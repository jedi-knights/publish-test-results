package ir

import "testing"

func TestStatus_String(t *testing.T) {
    cases := []struct {
        s    Status
        want string
    }{
        {StatusPassed, "passed"},
        {StatusFailed, "failed"},
        {StatusSkipped, "skipped"},
        {StatusError, "error"},
        {Status(99), "unknown"},
    }
    for _, tc := range cases {
        if got := tc.s.String(); got != tc.want {
            t.Errorf("Status(%d).String() = %q, want %q", tc.s, got, tc.want)
        }
    }
}
