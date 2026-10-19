package httpapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/errutil"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/github"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
)

func logIntegrationError(ctx context.Context, operation string, err error) {
	var githubErr *github.APIError
	var discordErr *discord.APIError
	var sqlErr interface{ SQLState() string }
	var diagnostic error
	switch {
	case errors.As(err, &githubErr):
		diagnostic = githubErr
	case errors.As(err, &discordErr):
		diagnostic = discordErr
	case errors.Is(err, context.DeadlineExceeded):
		diagnostic = context.DeadlineExceeded
	case errutil.OnlyMatches(err, context.Canceled):
		diagnostic = context.Canceled
	case errors.As(err, &sqlErr):
		diagnostic = fmt.Errorf("database error (SQLSTATE %s)", sqlErr.SQLState())
	default:
		diagnostic = fmt.Errorf("unclassified error (%T)", err)
	}
	logpkg.Error(ctx, fmt.Errorf("%s: %w", operation, diagnostic))
}
