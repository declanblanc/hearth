package media

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// R2Store writes objects to Cloudflare R2 using the S3-compatible API.
type R2Store struct {
	client     *s3.Client
	presigner  *s3.PresignClient
	bucket     string
	publicHost string
}

// NewR2 creates an R2Store. publicHost is the base URL for public object access
// (e.g. "https://assets.hearth.app"); leave empty if the bucket is not public.
func NewR2(accountID, accessKeyID, secretAccessKey, bucket, publicHost string) *R2Store {
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)),
		Region:       "auto",
		Credentials:  credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
	})
	return &R2Store{
		client:     client,
		presigner:  s3.NewPresignClient(client),
		bucket:     bucket,
		publicHost: publicHost,
	}
}

func (s *R2Store) Upload(ctx context.Context, key string, r io.Reader, contentType string) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        r,
		ContentType: aws.String(contentType),
	})
	return err
}

func (s *R2Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err
}

func (s *R2Store) URL(key string) string {
	if s.publicHost == "" || key == "" {
		return ""
	}
	return strings.TrimRight(s.publicHost, "/") + "/" + key
}

func (s *R2Store) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if key == "" {
		return "", nil
	}
	req, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", err
	}
	return req.URL, nil
}
