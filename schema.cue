package adhar

// adhar represents the top-level configuration for the Adhar platform.
adhar: {
  // cluster defines the configuration for the Kubernetes cluster.
  cluster: {
    // name is the name of the cluster. It must consist of alphanumeric characters and hyphens.
    name:        string & =~"^[a-zA-Z0-9-]+$"
    // host is the hostname or IP address of the cluster.
    host:        string & =~"^[a-zA-Z0-9.-]+$"
    // port is the port number to connect to the cluster. It must be between 0 and 65535.
    port:        int & >=0 & <=65535
    // provider is the cloud provider for the cluster. It must be one of the specified values.
    provider:    "digitalocean" | "gcp" | "azure" | "aws" | "civo"
    // region is the geographical region where the cluster is located.
    region:      string
    // kubeVersion is the version of Kubernetes running on the cluster. It must follow the semantic versioning format.
    kubeVersion: string & =~"^v?[0-9]+\\.[0-9]+\\.[0-9]+$"
    // nodePools is a list of node pools in the cluster.
    nodePools: [...{
      // name is the name of the node pool. It must consist of alphanumeric characters and hyphens.
      name:  string & =~"^[a-zA-Z0-9-]+$"
      // size is the size of the nodes in the pool. It must be one of the specified values.
      size:  "large" | "medium" | "small"
      // count is the number of nodes in the pool. It must be greater than 0.
      count: int & >0
      // tags is a list of tags associated with the node pool.
      tags:  [...string]
    }],
    // network defines the network configuration for the cluster.
    network: {
      // name is the name of the network. It must consist of alphanumeric characters and hyphens.
      name: string & =~"^[a-zA-Z0-9-]+$"
      // cidr is the CIDR block for the network. It must follow the CIDR notation format.
      cidr: string & =~"^([0-9]{1,3}\\.){3}[0-9]{1,3}/[0-9]{1,2}$"
    },
  },

  // apps defines the configuration for the applications deployed on the cluster.
  apps: {
    // adhar-console is the configuration for the Adhar Console application.
    "adhar-console": app & {
      // name is the name of the application. It must consist of alphanumeric characters and hyphens.
      name:        string & =~"^[a-zA-Z0-9-]+$"
      // description is a brief description of the application.
      description: string
      // contextPath is the context path where the application is accessible. It must start with a '/'.
      contextPath: string & =~"^/.*$"
    },
    // kyverno is the configuration for the Kyverno application.
    "kyverno": app & {
      // name is the name of the application. It must consist of alphanumeric characters and hyphens.
      name:        string & =~"^[a-zA-Z0-9-]+$"
      // description is a brief description of the application.
      description: string
      // contextPath is the context path where the application is accessible. It must start with a '/'.
      contextPath: string & =~"^/.*$"
    },
    // argo-workflows is the configuration for the Argo Workflows application.
    "argo-workflows": app & {
      // name is the name of the application. It must consist of alphanumeric characters and hyphens.
      name:        string & =~"^[a-zA-Z0-9-]+$"
      // description is a brief description of the application.
      description: string
      // contextPath is the context path where the application is accessible. It must start with a '/'.
      contextPath: string & =~"^/.*$"
    },
    // keycloak is the configuration for the Keycloak application.
    "keycloak": app & {
      // name is the name of the application. It must consist of alphanumeric characters and hyphens.
      name:        string & =~"^[a-zA-Z0-9-]+$"
      // description is a brief description of the application.
      description: string
      // contextPath is the context path where the application is accessible. It must start with a '/'.
      contextPath: string & =~"^/.*$"
    },
    // headlamp is the configuration for the Headlamp application.
    "headlamp": app & {
      // name is the name of the application. It must consist of alphanumeric characters and hyphens.
      name:        string & =~"^[a-zA-Z0-9-]+$"
      // description is a brief description of the application.
      description: string
      // contextPath is the context path where the application is accessible. It must start with a '/'.
      contextPath: string & =~"^/.*$"
    },
  },
}

// app defines the common structure for all applications.
app: {
  // name is the name of the application. It must consist of alphanumeric characters and hyphens.
  name:        string & =~"^[a-zA-Z0-9-]+$" | *"default-name"
  // description is a brief description of the application.
  description: string | *"default-description"
  // contextPath is the context path where the application is accessible. It must start with a '/'.
  contextPath: string & =~"^/.*$" | *"/default-path"
}