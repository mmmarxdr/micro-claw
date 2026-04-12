package content

import "fmt"

// humanSize formats a byte count as a human-readable string (e.g. "1.2 MB").
func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// FlattenBlocks produces a human-readable string for a Blocks slice.
// Text blocks are joined with "\n". Non-text blocks are replaced with a
// detailed placeholder, e.g.:
//
//	[image attached: photo.jpg, 1.2 MB, MIME image/jpeg, not processed by current model]
//
// This format is used when sending content to a text-only provider.
func FlattenBlocks(bs Blocks) string {
	if len(bs) == 0 {
		return ""
	}
	var parts []string
	for _, b := range bs {
		switch b.Type {
		case BlockText:
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		case BlockImage, BlockAudio, BlockDocument:
			var label string
			if b.Filename != "" {
				label = b.Filename
			} else {
				label = string(b.Type)
			}
			placeholder := fmt.Sprintf(
				"[%s attached: %s, %s, MIME %s, not processed by current model]",
				b.Type, label, humanSize(b.Size), b.MIME,
			)
			parts = append(parts, placeholder)
		default:
			// Generic placeholder for unknown future types.
			parts = append(parts, fmt.Sprintf("[%s attached, not processed by current model]", b.Type))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for i := 1; i < len(parts); i++ {
		result += "\n" + parts[i]
	}
	return result
}

// DegradationNotice returns a user-facing notice string describing what media
// could not be processed. Returns "" if bs contains no media blocks.
func DegradationNotice(bs Blocks) string {
	hasImage := false
	hasAudio := false
	for _, b := range bs {
		switch b.Type {
		case BlockImage:
			hasImage = true
		case BlockAudio:
			hasAudio = true
		}
	}
	switch {
	case hasImage && hasAudio:
		return "(I can't see images or listen to voice notes with the current model. I saved them for you.)"
	case hasImage:
		return "(I can't see images with the current model. I saved it for you.)"
	case hasAudio:
		return "(I can't listen to voice notes with the current model. I saved it for you.)"
	default:
		return "(Some media could not be processed with the current model. I saved it for you.)"
	}
}
