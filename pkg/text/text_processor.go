package text

import (
	"github.com/bbalet/stopwords"
)

func ProcessText(s string) string {
	s = stopwordsFilter(s)

	return s
}

func stopwordsFilter(s string) string {
	return stopwords.CleanString(s, "en", true)
}
