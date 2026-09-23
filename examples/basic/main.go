package main

import (
	"context"
	"fmt"
	"os"
	"time"

	sendrepute "github.com/sendrepute/sendrepute-go"
)

func main() {
	apiKey := os.Getenv("SENDREPUTE_API_KEY")
	if apiKey == "" {
		panic("SENDREPUTE_API_KEY is required")
	}
	client, err := sendrepute.NewClient(sendrepute.Config{
		APIKey: apiKey,
		// This explicit opt-in means each fresh input may be billed.
		PaidAnalysisConsent: true,
	})
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := client.Classify(ctx, sendrepute.ClassificationInput{
		Sender: "Example Team", Subject: "Welcome", Body: "Thanks for joining.",
	}, false)
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s %.3f\n", result.Result.Label, result.Result.SpamProbability)
}
