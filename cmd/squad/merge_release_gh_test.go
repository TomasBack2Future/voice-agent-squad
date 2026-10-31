package main

import "testing"

func TestChecksPassedRequiresEveryCheckCompletedSuccessfully(t *testing.T) {
	ok := checkResult{Status: "COMPLETED", Conclusion: "SUCCESS"}
	for name, c := range map[string]struct {
		in   []checkResult
		want bool
	}{
		"empty rollup":        {nil, false},
		"all success":         {[]checkResult{ok, {State: "SUCCESS"}}, true},
		"skipped and neutral": {[]checkResult{ok, {Status: "COMPLETED", Conclusion: "SKIPPED"}, {Status: "COMPLETED", Conclusion: "NEUTRAL"}}, true},
		"pending run":         {[]checkResult{ok, {Status: "IN_PROGRESS"}}, false},
		"queued":              {[]checkResult{ok, {Status: "QUEUED"}}, false},
		"failed":              {[]checkResult{ok, {Status: "COMPLETED", Conclusion: "FAILURE"}}, false},
		"cancelled":           {[]checkResult{ok, {Status: "COMPLETED", Conclusion: "CANCELLED"}}, false},
		"pending status ctx":  {[]checkResult{ok, {State: "PENDING"}}, false},
		"error status ctx":    {[]checkResult{{State: "ERROR"}}, false},
	} {
		if got := checksPassed(c.in); got != c.want {
			t.Errorf("%s: got %v want %v", name, got, c.want)
		}
	}
}
