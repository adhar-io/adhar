package localbuild

import (
	"testing"

	"github.com/adhar-io/adhar/api/v1alpha1"
	"github.com/stretchr/testify/assert"
)

func TestGiteaInternalBaseUrl(t *testing.T) {
	c := v1alpha1.BuildCustomizationSpec{
		Protocol:       "http",
		Port:           "8080",
		Host:           "adhar.localtest.me",
		UsePathRouting: false,
	}

	s := giteaInternalBaseUrl(c)
	assert.Equal(t, "http://gitea.adhar.localtest.me:8080", s)
	c.UsePathRouting = true
	s = giteaInternalBaseUrl(c)
	assert.Equal(t, "http://adhar.localtest.me:8080/gitea", s)
}
