//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/httpapi/apimcp"
	"github.com/omnara-ai/omnara/internal/redistore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
)

func TestAPIRateLimitChargesMCPToolCallsByOperation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	project := bootstrapPublicHTTPProject(t, newIntegrationServer(pool), "api-rate-limit")
	redisClient, err := redistore.NewWithTimeout(integrationredis.URL(t), time.Second)
	if err != nil {
		t.Fatalf("create redis client: %v", err)
	}
	t.Cleanup(func() { _ = redisClient.Close() })
	server := httptest.NewServer(newIntegrationServer(pool, WithAPIRateLimit(redisClient, 20, 0)))
	t.Cleanup(server.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   server.URL + apimcp.Path,
		HTTPClient: &http.Client{Transport: bearerTransport{token: project.AdminToken, next: http.DefaultTransport}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if _, err := session.ListTools(ctx, nil); err != nil {
		t.Fatalf("list tools: %v", err)
	}
	callTool := func(name string, arguments map[string]any) *mcp.CallToolResult {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
		if err != nil {
			t.Fatalf("call %s: %v", name, err)
		}
		return result
	}
	for range 2 {
		if result := callTool("whoami", nil); result.IsError {
			t.Fatalf("whoami within budget failed: %s", testutil.RequireType[*mcp.TextContent](t, result.Content[0]).Text)
		}
	}
	limited := callTool("whoami", nil)
	if text := testutil.RequireType[*mcp.TextContent](t, limited.Content[0]).Text; !limited.IsError ||
		!strings.Contains(text, `"rate_limited"`) || !strings.Contains(text, "retry after 180s") {
		t.Fatalf("over-budget whoami = %s, want a rate_limited tool error with the retry delay", text)
	}
	created := callTool("projects_create", map[string]any{"orgID": project.OrgID, "name": "rate-limited-writes"})
	if created.IsError {
		t.Fatalf("write after reads ran out failed: %s", testutil.RequireType[*mcp.TextContent](t, created.Content[0]).Text)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+openAPIBasePath+"/me", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+project.AdminToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("get me: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests || response.Header.Get("Retry-After") != "180" {
		t.Fatalf(
			"REST read after MCP reads ran out = %d with Retry-After %q, want 429 with 180",
			response.StatusCode,
			response.Header.Get("Retry-After"),
		)
	}
}
