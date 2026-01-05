package docker

import (
	"regexp"
	"strings"
)

type Dockerfile struct {
	Content     string
	BaseImage   string
	CUDAVersion string
	Arch        string
	Model       string // TODO: implement. call hf api and scrape model information
}

func ParseDockerfile(content string) (*Dockerfile, error) {
	dockerfile := &Dockerfile{}
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "FROM") {
			parts := strings.Split(line, " ")
			dockerfile.BaseImage = parts[1]
		}

		if strings.Contains(line, "aarch64") {
			dockerfile.Arch = "aarch64"
		} else {
			dockerfile.Arch = "amd64"
		}

		re := regexp.MustCompile(`(?i)CUDA(?:.*?)(?P<v>(\d{1,2})\.(\d)\.(\d))`)
		matches := re.FindStringSubmatch(line)
		if len(matches) > 0 {
			dockerfile.CUDAVersion = matches[1] // group "v"
		}

		/*
			re2 := regexp.MustCompile(`"((.*?)/([^"]+))"`)
			matches2 := re2.FindStringSubmatch(line)
			if len(matches2) > 0 {
				dockerfile.Model = matches2[1]
			}
		*/
	}
	return dockerfile, nil
}
