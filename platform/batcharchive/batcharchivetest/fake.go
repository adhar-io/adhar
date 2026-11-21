/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

// Package batcharchivetest is an in-memory S3 for the archive's tests: the
// five calls the archive makes (PUT/GET object, ListObjectsV2, PUT bucket),
// each one's SigV4 signature VERIFIED by re-signing it with the known key, so
// a test passes only if the client really signs what RustFS checks.
package batcharchivetest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// Store is the fake. Buckets and objects are public for assertions.
type Store struct {
	AccessKey, SecretKey string
	Server               *httptest.Server

	mu      sync.Mutex
	buckets map[string]map[string]object
	// Fail makes every request answer this status (0: behave).
	Fail int
	// PageSize is the ListObjectsV2 page size (default 1000).
	PageSize int
	// Requests counts the requests per method.
	Requests map[string]int
	// Now stamps LastModified; tests pin it.
	Now func() time.Time
}

type object struct {
	body     []byte
	modified time.Time
}

// New starts a fake with the given credential and buckets.
func New(accessKey, secretKey string, buckets ...string) *Store {
	s := &Store{AccessKey: accessKey, SecretKey: secretKey, buckets: map[string]map[string]object{}, Requests: map[string]int{}}
	for _, b := range buckets {
		s.buckets[b] = map[string]object{}
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Close stops the server.
func (s *Store) Close() { s.Server.Close() }

// URL is the endpoint to point a client at.
func (s *Store) URL() string { return s.Server.URL }

// Object returns an object's body (nil when absent).
func (s *Store) Object(bucket, key string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o, ok := s.buckets[bucket][key]; ok {
		return o.body
	}
	return nil
}

// Put stores an object directly, with a LastModified.
func (s *Store) Put(bucket, key string, body []byte, modified time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buckets[bucket] == nil {
		s.buckets[bucket] = map[string]object{}
	}
	s.buckets[bucket][key] = object{body: body, modified: modified}
}

// Keys lists a bucket's keys, sorted.
func (s *Store) Keys(bucket string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.buckets[bucket] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// HasBucket says whether a bucket exists.
func (s *Store) HasBucket(bucket string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.buckets[bucket]
	return ok
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func s3err(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code></Error>`, code)
}

// verify re-signs the request with the store's key over exactly the headers
// the client signed, at the client's own X-Amz-Date, and compares.
func (s *Store) verify(r *http.Request, body []byte) error {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		return fmt.Errorf("not a SigV4 request")
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	if r.Header.Get("X-Amz-Content-Sha256") != hash {
		return fmt.Errorf("payload hash header does not match the body")
	}
	signed := ""
	for _, part := range strings.Split(strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 "), ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "SignedHeaders=") {
			signed = strings.TrimPrefix(part, "SignedHeaders=")
		}
	}
	at, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		return fmt.Errorf("no X-Amz-Date")
	}
	u := *r.URL
	u.Scheme, u.Host = "http", r.Host
	clone, _ := http.NewRequest(r.Method, u.String(), nil)
	clone.Host = r.Host
	for _, h := range strings.Split(signed, ";") {
		if h == "host" {
			continue
		}
		if v, ok := r.Header[http.CanonicalHeaderKey(h)]; ok {
			clone.Header[http.CanonicalHeaderKey(h)] = v
		}
	}
	if strings.Contains(signed, "content-length") {
		clone.ContentLength = r.ContentLength
	}
	clone.Header.Del("X-Amz-Date")
	err = v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true }).SignHTTP(context.Background(),
		aws.Credentials{AccessKeyID: s.AccessKey, SecretAccessKey: s.SecretKey}, clone, hash, "s3", "us-east-1", at)
	if err != nil {
		return err
	}
	if clone.Header.Get("Authorization") != auth {
		return fmt.Errorf("SignatureDoesNotMatch")
	}
	return nil
}

func (s *Store) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.Requests[r.Method]++
	fail := s.Fail
	s.mu.Unlock()
	if fail != 0 {
		s3err(w, fail, "InternalError")
		return
	}
	if err := s.verify(r, body); err != nil {
		s3err(w, http.StatusForbidden, "SignatureDoesNotMatch")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(path, "/")
	s.mu.Lock()
	defer s.mu.Unlock()
	objs, ok := s.buckets[bucket]
	switch {
	case r.Method == http.MethodPut && key == "":
		if ok {
			s3err(w, http.StatusConflict, "BucketAlreadyOwnedByYou")
			return
		}
		s.buckets[bucket] = map[string]object{}
	case !ok:
		s3err(w, http.StatusNotFound, "NoSuchBucket")
	case r.Method == http.MethodPut:
		objs[key] = object{body: body, modified: s.now()}
	case r.Method == http.MethodGet && key != "":
		o, found := objs[key]
		if !found {
			s3err(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		_, _ = w.Write(o.body)
	case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
		s.list(w, objs, r.URL.Query().Get("prefix"), r.URL.Query().Get("continuation-token"))
	default:
		s3err(w, http.StatusBadRequest, "InvalidRequest")
	}
}

func (s *Store) list(w http.ResponseWriter, objs map[string]object, prefix, token string) {
	var keys []string
	for k := range objs {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	start, _ := strconv.Atoi(token)
	size := s.PageSize
	if size <= 0 {
		size = 1000
	}
	end := min(start+size, len(keys))
	type content struct {
		Key          string `xml:"Key"`
		LastModified string `xml:"LastModified"`
	}
	out := struct {
		XMLName               xml.Name  `xml:"ListBucketResult"`
		Contents              []content `xml:"Contents"`
		IsTruncated           bool      `xml:"IsTruncated"`
		NextContinuationToken string    `xml:"NextContinuationToken,omitempty"`
	}{}
	if start < len(keys) {
		for _, k := range keys[start:end] {
			out.Contents = append(out.Contents, content{Key: k, LastModified: objs[k].modified.UTC().Format(time.RFC3339Nano)})
		}
	}
	if end < len(keys) {
		out.IsTruncated = true
		out.NextContinuationToken = strconv.Itoa(end)
	}
	_ = xml.NewEncoder(w).Encode(out)
}
