package rag_test

import (
	"errors"
	"testing"

	"microagent/internal/rag"
)

// T1.2: verify interfaces compile, errors are distinct.

func TestErrors_Distinct(t *testing.T) {
	errs := []error{
		rag.ErrDocNotFound,
		rag.ErrUnsupportedMIME,
		rag.ErrStorageLimitReached,
	}
	for i, e := range errs {
		if e == nil {
			t.Errorf("error[%d] is nil", i)
		}
		for j, other := range errs {
			if i != j && errors.Is(e, other) {
				t.Errorf("error[%d] and error[%d] should be distinct but errors.Is returned true", i, j)
			}
		}
	}
}

// Compile-time check: verify interface satisfaction can be tested.
// These are nil-interface assignment checks — they don't run logic but
// confirm the interface types are defined correctly.
var _ rag.Extractor = (*mockExtractor)(nil)
var _ rag.Chunker = (*mockChunker)(nil)
var _ rag.DocumentStore = (*mockDocumentStore)(nil)
