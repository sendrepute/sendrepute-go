package sendrepute

import (
	"context"
	"net/url"
	"testing"
)

func TestCustomerServiceContract(t *testing.T) {
	if len(serviceOperations) != 49 {
		t.Fatalf("contract count: %d", len(serviceOperations))
	}
	c, err := NewClient(Config{APIKey: "offline-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Request(context.Background(), "customerPurchaseVip", ServiceInput{})
	if err == nil {
		t.Fatal("mutation without consent was accepted")
	}
	_, err = c.Request(context.Background(), "arbitraryURL", ServiceInput{})
	if err == nil {
		t.Fatal("unlisted operation was accepted")
	}
	_, err = c.Request(context.Background(), "customerGetAccount", ServiceInput{Query: url.Values{"redirect": {"https://outside.test"}}})
	if err == nil {
		t.Fatal("unlisted query was accepted")
	}
}
