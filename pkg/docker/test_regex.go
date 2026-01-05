package docker

import (
	"fmt"
	"regexp"
)

func main() {
	re := regexp.MustCompile(`(?i)CUDA(?:.*?)(?P<v>(\d{1,2})\.(\d)\.(\d))`)

	testLines := []string{
		"FROM nvidia/cuda:11.8.0-devel-ubuntu20.04",
		"ENV CUDA_VERSION=12.1.0",
		"# CUDA 10.2.89 runtime",
		"RUN apt-get install cuda-toolkit-11-8",
	}

	for _, line := range testLines {
		fmt.Printf("Testing line: %s\n", line)
		matches := re.FindStringSubmatch(line)
		if len(matches) > 0 {
			fmt.Printf("  Full match: %s\n", matches[0])
			fmt.Printf("  Named group 'v': %s\n", matches[re.SubexpIndex("v")])
			fmt.Printf("  By index matches[1]: %s\n", matches[1])
			fmt.Printf("  Individual version parts: %s.%s.%s\n", matches[2], matches[3], matches[4])
		} else {
			fmt.Println("  No match")
		}
		fmt.Println()
	}
}
