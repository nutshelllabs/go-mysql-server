// Command guard runs custom vet checks for this repository.
//
// Standard checks stay with installable tooling:
//
//	gofmt -l <pkgs>
//	go vet <pkgs>
//	dupl -threshold 80 <pkgs>
//
// The analyzers below encode this change's lessons, so rerunning them
// warns before the bad patterns return:
//
//	docname   : exported declaration docs start with the declared name.
//	likeescape: LIKE wildcard handling lives in the parse-time decision;
//	            do not re-scan patterns with strings.Count on "_", "%",
//	            "\_" or "\%".
//
// Usage:
//
//	go run ./cmd/guard ./sql/...
//	go vet -vettool=$(go build -o /tmp/guard ./cmd/guard) ./sql/...
package main

import (
	"golang.org/x/tools/go/analysis/multichecker"
)

func main() {
	multichecker.Main(
		docnameAnalyzer,
		likeescapeAnalyzer,
	)
}
