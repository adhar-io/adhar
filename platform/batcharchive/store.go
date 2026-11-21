/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

// Package batcharchive is the run-summary archive of design 0025 Phase 4: the
// final CloudEvent of every finished batch run, kept in the platform object
// store long after the `adhar.batch` topic (90 days) has let it go.
//
// # One object per run, at a key a reader can compute
//
//	s3://adhar-batch-archive/<namespace>/<batchJob>/<businessDate>/<run>.json
//
// The object IS the run's final `adhar.batch` event — `run.succeeded`,
// `run.failed`, `run.skipped`, or the `run.lost` of a run that vanished — with
// the same `id` it carried on the bus, so a reader that has both sources drops
// the duplicate by id and every parser of the bus reads the archive unchanged.
// The key is the run's identity, not the event's: a run observed again (the
// observer restarted between its record and its publish, or a stopped run that
// later finished) overwrites its own object, and the archive holds one answer
// per run — the last one.
//
// # Plain S3, signed here
//
// RustFS speaks S3 over SigV4. The few calls this needs (PUT/GET an object,
// ListObjectsV2, PUT a bucket) are signed with aws-sdk-go-v2's signer, which the
// module already carries, rather than adding the S3 SDK for four requests.
// Path-style addressing, because RustFS is reached by a Service name.
package batcharchive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const (
	// DefaultBucket is the archive bucket. The rustfs package's bucket Job
	// creates it, and the writer creates it too on NoSuchBucket, so an archive
	// switched on before that Job re-ran still lands.
	DefaultBucket = "adhar-batch-archive"
	// DefaultEndpoint is the platform object store inside the cluster.
	DefaultEndpoint = "http://rustfs.adhar-system.svc.cluster.local:9000"
	// DefaultRegion is what RustFS signs with; any region is accepted, but
	// the signature must name one.
	DefaultRegion = "us-east-1"
	// CredentialSecret is the platform object-store credential contract
	// (data/rustfs: Secret root-creds, keys rootUser / rootPassword).
	CredentialSecret    = "root-creds"
	CredentialAccessKey = "rootUser"
	CredentialSecretKey = "rootPassword"
	// Undated is the date segment of a run whose business date is not an ISO
	// date. Never the raw value: it comes from the run's parameters, which a
	// tenant writes, and must not be able to add path segments to the key.
	Undated = "undated"
)

var isoDate = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

// dnsName is what a namespace, a BatchJob and a run Job are named with. A key
// segment that is not one is refused rather than escaped: every real one is.
var dnsName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// DateSegment is the business-date segment of a key.
func DateSegment(businessDate string) string {
	if isoDate.MatchString(businessDate) {
		return businessDate
	}
	return Undated
}

// Key is where a run's summary is archived.
func Key(namespace, batchJob, businessDate, run string) (string, error) {
	for _, s := range []string{namespace, batchJob, run} {
		if !dnsName.MatchString(s) || strings.Contains(s, "..") {
			return "", fmt.Errorf("%q cannot be an archive key segment", s)
		}
	}
	return fmt.Sprintf("%s/%s/%s/%s.json", namespace, batchJob, DateSegment(businessDate), run), nil
}

// JobPrefix is the prefix every archived run of one BatchJob shares.
func JobPrefix(namespace, batchJob string) string {
	return namespace + "/" + batchJob + "/"
}

// DateOfKey is the business-date segment of a key ("" when it has none).
func DateOfKey(key string) string {
	parts := strings.Split(key, "/")
	if len(parts) != 4 {
		return ""
	}
	return parts[2]
}

// Credentials are an S3 access key pair.
type Credentials struct{ AccessKey, SecretKey string }

// Client is the archive's S3 client. Endpoint is the store's base URL
// (scheme://host:port); HTTP is the client to send with.
type Client struct {
	Endpoint string
	Bucket   string
	Region   string
	HTTP     *http.Client
	// Credentials is asked on every request, so a credential that rotates or
	// appears late (the manager starts before RustFS has published it) is
	// picked up without a restart.
	Credentials func(ctx context.Context) (Credentials, error)
	// Now is the signing clock; tests pin it.
	Now func() time.Time
}

// ErrNoSuchBucket is the store saying the bucket does not exist.
var ErrNoSuchBucket = errors.New("NoSuchBucket")

// ErrNotFound is a missing object.
var ErrNotFound = errors.New("NoSuchKey")

// StatusError is any other answer that is not a success.
type StatusError struct {
	Status int
	Code   string
	Body   string
}

func (e *StatusError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("object store answered HTTP %d %s", e.Status, e.Code)
	}
	return fmt.Sprintf("object store answered HTTP %d", e.Status)
}

func (c *Client) bucket() string {
	if c.Bucket != "" {
		return c.Bucket
	}
	return DefaultBucket
}

func (c *Client) region() string {
	if c.Region != "" {
		return c.Region
	}
	return DefaultRegion
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// do signs and sends one request. path is below the endpoint ("/bucket/key").
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body []byte, contentType string) (*http.Response, error) {
	if c.Credentials == nil {
		return nil, errors.New("no object-store credential configured")
	}
	creds, err := c.Credentials(ctx)
	if err != nil {
		return nil, err
	}
	if creds.AccessKey == "" || creds.SecretKey == "" {
		return nil, errors.New("the object-store credential is empty")
	}
	u, err := url.Parse(strings.TrimRight(c.Endpoint, "/") + path)
	if err != nil {
		return nil, err
	}
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	// RustFS requires the payload hash header on every signed call.
	req.Header.Set("X-Amz-Content-Sha256", hash)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.ContentLength = int64(len(body))
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	err = v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true }).SignHTTP(ctx,
		aws.Credentials{AccessKeyID: creds.AccessKey, SecretAccessKey: creds.SecretKey},
		req, hash, "s3", c.region(), now())
	if err != nil {
		return nil, err
	}
	return c.httpClient().Do(req)
}

type s3Error struct {
	Code string `xml:"Code"`
}

func failure(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e s3Error
	_ = xml.Unmarshal(raw, &e)
	switch {
	case e.Code == "NoSuchBucket":
		return ErrNoSuchBucket
	case e.Code == "NoSuchKey" || (resp.StatusCode == http.StatusNotFound && e.Code == ""):
		return ErrNotFound
	}
	return &StatusError{Status: resp.StatusCode, Code: e.Code, Body: string(raw)}
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
}

// Put writes one object.
func (c *Client) Put(ctx context.Context, key string, body []byte) error {
	resp, err := c.do(ctx, http.MethodPut, "/"+c.bucket()+"/"+key, nil, body, "application/cloudevents+json")
	if err != nil {
		return err
	}
	defer drain(resp)
	if resp.StatusCode >= 300 {
		return failure(resp)
	}
	return nil
}

// MakeBucket creates the bucket; one that already exists is a success.
func (c *Client) MakeBucket(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodPut, "/"+c.bucket(), nil, nil, "")
	if err != nil {
		return err
	}
	defer drain(resp)
	if resp.StatusCode < 300 || resp.StatusCode == http.StatusConflict {
		return nil
	}
	return failure(resp)
}

// Get reads one object.
func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, "/"+c.bucket()+"/"+key, nil, nil, "")
	if err != nil {
		return nil, err
	}
	defer drain(resp)
	if resp.StatusCode >= 300 {
		return nil, failure(resp)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxObject))
}

// maxObject bounds what one archived summary may be. A run's final event is
// a few KiB (the termination summary itself is capped at 3,584 bytes).
const maxObject = 1 << 20

// Object is one listed key and when it was last written. An archived run's
// event time is never after its object's LastModified (the object is written
// after the run ended), which is what lets a reader skip objects by date.
type Object struct {
	Key          string
	LastModified time.Time
}

type listResult struct {
	Contents []struct {
		Key          string `xml:"Key"`
		LastModified string `xml:"LastModified"`
	} `xml:"Contents"`
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
}

// List returns every object under prefix, in key order. maxKeys bounds the
// total (0: no bound); the listing is paged with ListObjectsV2.
func (c *Client) List(ctx context.Context, prefix string, maxKeys int) ([]Object, error) {
	var objs []Object
	token := ""
	for page := 0; page < 1000; page++ {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := c.do(ctx, http.MethodGet, "/"+c.bucket(), q, nil, "")
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 300 {
			err := failure(resp)
			drain(resp)
			return nil, err
		}
		var r listResult
		err = xml.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&r)
		drain(resp)
		if err != nil {
			return nil, fmt.Errorf("reading the archive listing: %w", err)
		}
		for _, o := range r.Contents {
			t, _ := time.Parse(time.RFC3339Nano, o.LastModified)
			objs = append(objs, Object{Key: o.Key, LastModified: t})
		}
		if !r.IsTruncated || r.NextContinuationToken == "" || (maxKeys > 0 && len(objs) >= maxKeys) {
			break
		}
		token = r.NextContinuationToken
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].Key < objs[j].Key })
	return objs, nil
}
