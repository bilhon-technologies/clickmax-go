package clickmax_test

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"clickmax.io/go"
)

func ExampleNew() {
	c, err := clickmax.New(os.Getenv("CLICKMAX_API_KEY"), clickmax.WithTimeout(30*time.Second))
	if err != nil {
		log.Fatal(err)
	}

	tags, err := c.Tags.List(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%d tags", len(tags.Data))
}

func ExampleTagsService_ManualList() {
	c, err := clickmax.New(os.Getenv("CLICKMAX_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}

	page, err := c.Tags.ManualList(context.Background(), clickmax.TagsManualListParams{
		Name:    clickmax.Ptr("vip"),
		PerPage: clickmax.Ptr[int64](50),
		Order:   clickmax.Ptr(clickmax.TagsManualListParamsOrderDesc),
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%+v", page)
}

func ExampleNullable() {
	c, err := clickmax.New(os.Getenv("CLICKMAX_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}

	// Clears the description, sets the color and leaves the group name unchanged.
	_, err = c.Pipelines.Update(context.Background(), clickmax.PipelinesUpdateInput{
		ID:          "0b7f4c1e-5d2a-4f3b-9c8d-1a2b3c4d5e6f",
		Description: clickmax.Null[string](),
		Color:       clickmax.NewNullable("ff8800"),
	})
	if err != nil {
		log.Fatal(err)
	}
}

func ExampleError() {
	c, err := clickmax.New(os.Getenv("CLICKMAX_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}

	_, err = c.Tags.Create(context.Background(), clickmax.TagsCreateInput{Name: "VIP"})
	var cerr *clickmax.Error
	switch {
	case err == nil:
	case errors.As(err, &cerr) && cerr.Type == clickmax.ErrorTypeInvalidRequest:
		log.Printf("input rejected: code=%s issues=%v", cerr.Code, cerr.Issues)
	case errors.As(err, &cerr) && cerr.Type == clickmax.ErrorTypeRateLimit:
		log.Printf("rate limited, retry in %s", cerr.RetryAfter)
	default:
		log.Printf("tags create failed: %v", err)
	}
}
