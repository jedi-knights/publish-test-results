package publish

import (
    "context"
    "fmt"

    "github.com/jedi-knights/publish-test-results/internal/ir"
)

// Config is what the caller supplies per publish. Everything the
// GitHub API needs beyond the test data itself.
type Config struct {
    CheckName string
    HeadSHA   string
    // Options gates whether per-status annotations are emitted.
    Options Options
}

// Publish creates a check-run for the results, then PATCHes it once
// per additional annotation batch. Returns the created check-run so
// callers can log the HTMLURL.
func Publish(ctx context.Context, client *Client, cfg Config, results []ir.TestResult) (*CheckRunResponse, error) {
    if cfg.HeadSHA == "" {
        return nil, fmt.Errorf("HeadSHA is required")
    }
    if cfg.CheckName == "" {
        cfg.CheckName = "Test Results"
    }

    totals := Compute(results)
    annotations := AnnotationsFor(results, cfg.Options)
    batches := Chunk(annotations, MaxAnnotationsPerRequest)

    output := Output{
        Title:   Title(totals),
        Summary: SummaryMarkdown(results),
        Text:    BodyMarkdown(results),
    }
    if len(batches) > 0 {
        output.Annotations = batches[0]
    }

    createBody := CheckRunCreate{
        Name:       cfg.CheckName,
        HeadSHA:    cfg.HeadSHA,
        Status:     "completed",
        Conclusion: Conclusion(totals),
        Output:     output,
    }

    resp, err := client.CreateCheckRun(ctx, createBody)
    if err != nil {
        return nil, fmt.Errorf("create check-run: %w", err)
    }

    // PATCH additional batches, if any. Only the Annotations field is
    // re-sent per batch; the summary/text stay as posted at creation.
    for i := 1; i < len(batches); i++ {
        update := CheckRunUpdate{
            Output: Output{
                Title:       output.Title,
                Summary:     output.Summary,
                Annotations: batches[i],
            },
        }
        if _, err := client.UpdateCheckRun(ctx, resp.ID, update); err != nil {
            return resp, fmt.Errorf("update check-run batch %d: %w", i+1, err)
        }
    }
    return resp, nil
}
