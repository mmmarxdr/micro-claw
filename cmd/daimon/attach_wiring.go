package main

import (
	"context"

	"daimon/internal/channel"
	"daimon/internal/rag"
)

// newAttachmentDocExtractor builds the document-extractor used by the
// WebChannel to inline text from PDF/DOCX/markdown/plain attachments before
// the message reaches the provider. Stateless and always available — no
// dependency on RAG being enabled, since these are two distinct features
// (RAG ingests user-curated docs into a search index; attachment extraction
// gives the model access to a one-off file the user just dropped into chat).
//
// The chain mirrors rag_wiring.go's ingestion extractor (pdftotext first
// for LaTeX/CID-encoded PDFs when poppler-utils is installed, then pure-Go
// ledongthuc for the rest, then DOCX, then markdown/plain). When pdftotext
// is absent its Supports() returns false and the chain falls through.
func newAttachmentDocExtractor() channel.DocExtractor {
	return ragExtractorAdapter{
		inner: rag.NewSelectExtractor(
			rag.PdftotextExtractor{},
			rag.PdfExtractor{},
			rag.DocxExtractor{},
			rag.MarkdownExtractor{},
			rag.PlainTextExtractor{},
		),
	}
}

// ragExtractorAdapter wraps rag.SelectExtractor so the channel package does
// not import rag directly. The two interfaces only diverge in the return
// shape (rag.ExtractedDoc vs plain string) — this adapter narrows it.
type ragExtractorAdapter struct {
	inner *rag.SelectExtractor
}

func (a ragExtractorAdapter) Supports(mime string) bool {
	return a.inner.Supports(mime)
}

func (a ragExtractorAdapter) Extract(ctx context.Context, data []byte, mime string) (string, error) {
	doc, err := a.inner.Extract(ctx, data, mime)
	if err != nil {
		return "", err
	}
	return doc.Text, nil
}
