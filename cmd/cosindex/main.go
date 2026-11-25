// Command cosindex runs on the publisher only. stdout is the JSON catalog.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"

	"envinit/internal/cosstore"
	cos "github.com/tencentyun/cos-go-sdk-v5"
)

func run() error {
	c := cosstore.Config{Provider: "cos"}
	flag.StringVar(&c.Bucket, "bucket", "", "COS bucket")
	flag.StringVar(&c.Region, "region", "", "COS region")
	flag.StringVar(&c.Endpoint, "endpoint", "", "regional COS endpoint")
	prefix := flag.String("prefix", "", "material prefix")
	flag.Parse()
	id, key := os.Getenv("COS_SECRET_ID"), os.Getenv("COS_SECRET_KEY")
	if id == "" || key == "" {
		return fmt.Errorf("publisher COS credentials are required")
	}
	client, err := c.Client(&http.Client{Transport: &cos.AuthorizationTransport{SecretID: id, SecretKey: key, SessionToken: os.Getenv("COS_SESSION_TOKEN")}})
	if err != nil {
		return err
	}
	files, err := cosstore.List(context.Background(), client, *prefix)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(files)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
