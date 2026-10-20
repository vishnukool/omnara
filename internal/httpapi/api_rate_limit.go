package httpapi

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/apimcp"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/redistore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
)

type apiRateLimit struct {
	limiter httpauth.RateLimiter
	perHour int
	burst   int
}

func WithAPIRateLimit(client *redistore.Client, perHour, burst int) Option {
	if burst == 0 {
		burst = max(perHour/10, 1)
	}
	return func(s *Server) {
		s.apiRateLimit = &apiRateLimit{limiter: httpauth.NewRedisRateLimiter(client), perHour: perHour, burst: burst}
	}
}

func (s *Server) limitAPIRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != apimcp.Path && !s.apiRateLimit.allow(r) {
			retryAfter := strconv.Itoa(int(math.Ceil(time.Hour.Seconds() / float64(s.apiRateLimit.perHour))))
			w.Header().Set("Retry-After", retryAfter)
			apierror.Write(w, openapi.ErrorCodeRateLimited, "retry after "+retryAfter+"s")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (l *apiRateLimit) allow(r *http.Request) bool {
	if l == nil {
		return true
	}
	principal, ok := principalFromContext(r.Context())
	if !ok {
		return true
	}
	account, ok := rateLimitAccount(principal)
	if !ok {
		return true
	}
	bucket := "write"
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		bucket = "read"
	}
	key := "api:" + strconv.Itoa(l.perHour) + ":" + strconv.Itoa(l.burst) + ":" + account + ":" + bucket
	window := time.Duration(l.burst) * time.Hour / time.Duration(l.perHour)
	err := l.limiter.Allow(context.WithoutCancel(r.Context()), key, l.burst, window)
	switch {
	case errors.Is(err, httpauth.ErrRateLimited):
		logpkg.Attach(r.Context(), logpkg.Fields{"rate_limit.result": "limited", "rate_limit.account": account})
		return false
	case err != nil:
		logpkg.Attach(r.Context(), logpkg.Fields{
			"rate_limit.result":  "error",
			"rate_limit.account": account,
			"rate_limit.error":   err.Error(),
		})
	}
	return true
}

func rateLimitAccount(principal identitystore.PrincipalRecord) (string, bool) {
	switch {
	case principal.OrgAPIKeyID != uuid.Nil:
		return "org:" + principal.OrgID.String(), true
	case principal.PersonalAccessTokenID != uuid.Nil, principal.OAuthAccessTokenID != uuid.Nil:
		return "user:" + principal.ID.String(), true
	default:
		return "", false
	}
}
