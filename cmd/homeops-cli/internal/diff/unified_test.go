package diff

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUnifiedUsesLCSAndPreservesRenderedBytes(t *testing.T) {
	before := []byte("one\ntwo\nthree\n")
	after := []byte("one\nnew\nthree\n")
	assert.Equal(t, "--- a/example.txt\n+++ b/example.txt\n@@ -1,3 +1,3 @@\n one\n-two\n+new\n three\n", Unified("example.txt", before, after))
}

func TestUnifiedContextPreservesUpgradePlanHunkShape(t *testing.T) {
	before := []byte("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven\n")
	after := []byte(strings.Replace(string(before), "eight", "changed", 1))
	assert.Equal(t, "--- a/plan.yaml\n+++ b/plan.yaml\n@@ -5,7 +5,7 @@\n five\n six\n seven\n-eight\n+changed\n nine\n ten\n eleven\n", UnifiedContext("plan.yaml", before, after, 3))
}
