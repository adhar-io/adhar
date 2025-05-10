package providers

import (
	"errors"
	"fmt"
)

// Enhanced GiteaProvider with error handling and authentication.
type GiteaProvider struct {
	BaseURL string
	Token   string
}

func (g *GiteaProvider) Configure() error {
	if g.BaseURL == "" || g.Token == "" {
		return errors.New("missing Gitea configuration: BaseURL or Token")
	}
	fmt.Println("Configuring Gitea provider with BaseURL:", g.BaseURL)
	return nil
}

func (g *GiteaProvider) GetRepositoryURL(repoName string) (string, error) {
	if repoName == "" {
		return "", errors.New("repository name cannot be empty")
	}
	return fmt.Sprintf("%s/%s", g.BaseURL, repoName), nil
}

func (g *GiteaProvider) CreateRepository(repoName string) error {
	if repoName == "" {
		return errors.New("repository name cannot be empty")
	}
	fmt.Printf("Creating repository %s in Gitea at %s\n", repoName, g.BaseURL)
	return nil
}

// GitLabProvider is an implementation of the GitProvider interface for GitLab.
type GitLabProvider struct{}

func (g *GitLabProvider) Configure() error {
	fmt.Println("Configuring GitLab provider")
	return nil
}

func (g *GitLabProvider) GetRepositoryURL(repoName string) (string, error) {
	return fmt.Sprintf("https://gitlab.example.com/%s", repoName), nil
}

func (g *GitLabProvider) CreateRepository(repoName string) error {
	fmt.Printf("Creating repository %s in GitLab\n", repoName)
	return nil
}

// GithubProvider is an implementation of the GitProvider interface for GitHub.
type GithubProvider struct{}

func (g *GithubProvider) Configure() error {
	fmt.Println("Configuring GitHub provider")
	return nil
}

func (g *GithubProvider) GetRepositoryURL(repoName string) (string, error) {
	return fmt.Sprintf("https://github.com/%s", repoName), nil
}

func (g *GithubProvider) CreateRepository(repoName string) error {
	fmt.Printf("Creating repository %s in GitHub\n", repoName)
	return nil
}

// BitbucketProvider is an implementation of the GitProvider interface for Bitbucket.
type BitbucketProvider struct{}

func (b *BitbucketProvider) Configure() error {
	fmt.Println("Configuring Bitbucket provider")
	return nil
}

func (b *BitbucketProvider) GetRepositoryURL(repoName string) (string, error) {
	return fmt.Sprintf("https://bitbucket.org/%s", repoName), nil
}

func (b *BitbucketProvider) CreateRepository(repoName string) error {
	fmt.Printf("Creating repository %s in Bitbucket\n", repoName)
	return nil
}
