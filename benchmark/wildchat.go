package benchmark

import (
	"bufio"
	"encoding/json"
	"os"
)

type Article struct {
	Article       string `json:"article"`
	SummaryTokens int    `json:"summary_tokens"`
}

func getRequestArticles() error {
	wildchatFile, error := os.Open("./wildchat_flat.jsonl")
	if error != nil {
		return error
	}

	defer wildchatFile.Close()

	articles := []Article{}
	scanner := bufio.NewScanner(wildchatFile)
	for scanner.Scan() {
		articleText := scanner.Text()
		article := Article{}
		error := json.Unmarshal([]byte(articleText), &article)
		if error != nil {
			return error
		}

		articles = append(articles, article)
	}

	// loop through each article and print the article and summary tokens
	for _, article := range articles {
		println(article.Article, article.SummaryTokens)
	}

	return nil
}
