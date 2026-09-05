package converter

import "github.com/levmv/polka/internal/epubtest"

type epubLinkProblem = epubtest.Problem

func checkEPUBInternalLinks(raw []byte) ([]epubLinkProblem, error) {
	return epubtest.Check(raw)
}
