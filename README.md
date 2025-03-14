![Adhar Logo](docs/imgs/adhar-logo-white.svg#gh-dark-mode-only)
![Adhar Logo](docs/imgs/adhar-logo-black.svg#gh-light-mode-only)

<div align="center">

# ADHAR - The Open Foundation

</div>

<p align="center">
  <a href="https://join.slack.com/t/adharworkspace/shared_invite/zt-26586j9sx-QGrIejNigvzGJrnyH~IXww"><img src="https://img.shields.io/badge/slack--channel-blue?logo=slack"></a>
  <a href="https://github.com/adhar-io/adhar/releases/"><img alt="Releases" src="https://img.shields.io/github/release-date/adhar-io/adhar?label=latest%20release" /></a>
  <a href="https://hub.docker.com/r/adhario/adhar"><img alt="Docker pulls" src="https://img.shields.io/docker/pulls/adhario/adhar" /></a>
  <a href="https://img.shields.io/github/adhar-io/adhar/actions/workflows/main.yml"><img alt="Build status" src="https://img.shields.io/github/actions/workflow/status/adhar-io/adhar/e2e.yaml" /></a>
  <a href="https://img.shields.io/github/last-commit/adhar-io/adhar"><img alt="Last commit" src="https://img.shields.io/github/last-commit/adhar-io/adhar" /></a>
  <a href="https://img.shields.io/crates/l/ap"><img alt="License" src="https://img.shields.io/crates/l/ap" /></a>
  <a href="https://adhar.io/"><img src="https://img.shields.io/website-up-down-green-red/http/shields.io.svg" alt="Website adhar.io"></a>
</p>

> :bulb: `Adhar`, derived from the Sanskrit word for `Foundation` is a transformative Internal Developer Platform (IDP) that redefines software development. By seamlessly integrating industry-leading open-source technologies and embracing cloud-native principles, `Adhar` provides a ***Scalable*** and ***Efficient*** environment for developing complex, connected, ever-changing applications. With a strong emphasis on ***Security***, ***Governance***, ***AI Assistance***  and ***Developer productivity***, `Adhar` empowers teams to innovate rapidly and deliver exceptional software solutions with ease.

> :warning: **CAUTION:** This project is in early development stage and not ready for production. Use it only for testing and experimentation.

## Adhar Platform Goals :dart:

1. **Comprehensive All-in-One Integrated Platform:** The platform encompasses the entire software development lifecycle, from defining requirements and designing solutions to developing, testing, and deploying applications. This unified approach eliminates the need for switching between disparate tools, improving efficiency and collaboration.

2. **Enhanced Developer and Operator Experience:** The platform prioritizes developer and operator experience by providing intuitive interfaces, automated tasks, and streamlined workflows. This user-centric design reduces friction and empowers users to focus on their core responsibilities.

3. **Clear Responsibility Segregation:** The platform establishes clear boundaries between application teams and platform teams, promoting a well-defined division of labor. This separation of concerns ensures that each team focuses on their respective areas of expertise, enhancing overall efficiency.

4. **Holistic Governance and Compliance:** The platform incorporates robust governance and compliance mechanisms to ensure adherence to regulatory requirements and maintain data integrity. This built-in compliance framework minimizes risk and promotes transparency.

5. **Platform-as-a-Product Approach:** The platform adopts a product-centric approach, eliminating the need for each organization to reinvent the wheel. This shared infrastructure model reduces development costs and promotes standardization across the industry.

6. **Polyglot Technology Stack:** The platform embraces a polyglot approach, supporting a wide range of programming languages, frameworks, and cloud environments. This flexibility allows developers to choose the tools that best suit their needs, fostering innovation.

7. **Continuous Evolution:** The platform remains perpetually evolving, incorporating modern technology trends and industry best practices. This commitment to continuous improvement ensures that the platform remains relevant and cutting-edge.

8. **Optimal Open Source Cloud-Native Integration:** The platform involves seamlessly combining open-source tools and technologies to build, deploy, and manage cloud-native applications. This integration enables organizations to leverage the flexibility, scalability, and cost-effectiveness of cloud computing while maintaining control over their infrastructure and data.

9. **GitOps for Infrastructure and Applications:** The platform adheres to GitOps principles for managing both infrastructure and applications. This declarative approach ensures consistency, reproducibility, and simplified configuration management.

10. **Self-Service Resource Provisioning:** The platform empowers users to provision resources on-demand, eliminating the need for manual intervention. This self-service model enhances agility and reduces administrative overhead.

11. **AI Powered Low-Code Platform:** Adhar's low-code application development capabilities can empower non-technical users to create simple applications without extensive coding knowledge, broadening the pool of contributors and accelerating application development. AI can automate repetitive tasks, provide code recommendations, and accelerate application testing, leading to faster and more efficient software development.

12. **Fully Open Source:** The platform embraces open-source principles, fostering transparency, collaboration, and continuous improvement. This open-source philosophy aligns with the values of the developer community and promotes innovation.

## Getting Started :sparkles:

### Prerequisites

Before you begin, ensure you have the following tools installed on your system:

- **Docker**: Required for running containers. [Install Docker](https://docs.docker.com/get-docker/)
- **kubectl**: Command-line tool for interacting with Kubernetes clusters. [Install kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl/)

### Installation

The following command can be used as a convenience for installing `adhar`, (be sure to check the script first if you are concerned):

```bash
# Install Adhar CLI
curl -fsSL https://raw.githubusercontent.com/adhar-io/adhar/main/hack/install.sh | bash

# Verify installation
adhar version

# Example output
# adhar 0.4.1 go1.21.5 linux/amd64
```

Alternatively, you can download the latest binary from [the latest release page](https://github.com/adhar-io/adhar/releases/latest).

Once you have `adhar` cli installed, the most basic command which creates a Kubernetes Cluster (Kind cluster) with the core packages installed.

```bash
adhar up
```
To teardown whole cluster, can run:
```bash
adhar down
```

Access the `Adhar Console` using following URL and credentials:

```
https://adhar.localtest.me:8443/

Credentials:
- user1 / Keyclaok USER_PASSWORD (Admin User)
- user2 / Keyclaok USER_PASSWORD (Normal User)
```

<details>
  <summary>What are the core packages?</summary>

  * **ArgoCD** is the GitOps solution to deploy manifests to Kubernetes clusters. In this project, a package is an ArgoCD application. 
  * **Gitea** server is the in-cluster Git server that ArgoCD can be configured to sync resources from. You can sync from local file systems to this.
  * **Ingress-nginx** is used as a method to access in-cluster resources such as ArgoCD UI and Gitea UI.

  The default manifests for the core packages are available [here](pkg/controllers/localbuild/resources).
  See the [contribution doc](./CONTRIBUTING.md) for more information on how core packages are installed and configured.

</details>


Once `adhar` finishes provisioning cluster and packages, you can access GUIs by going to the following addresses in your browser.

* ***ArgoCD***: https://adhar.localtest.me:8443/argocd/  (Get the username and password from above command)
* ***Gitea***: https://adhar.localtest.me:8443/gitea/ (Get the username and password from above command)
* ***Keycloak***: https://adhar.localtest.me:8443/keycloak/ (Username is `adhar-admin` and password `KEYCLOAK_ADMIN_PASSWORD` retrieved from above command)
* ***Argo-Workflows***: https://adhar.localtest.me:8443/argo-workflows/
* ***Adhar Console***: https://adhar.localtest.me:8443/ (Username is `user1` or `user2` and password `USER-PASSWORD` retrieved from above command Keycloak section)
* ***Headlamp***: https://adhar.localtest.me:8443/headlamp/ (Username is `user1` or `user2` and password `USER-PASSWORD` retrieved from above command Keycloak section)
* ***JupyterHub***: https://adhar.localtest.me:8443/jupyterhub/ (Username is `user1` or `user2` and password `USER-PASSWORD` retrieved from above command Keycloak section)

#### Secrets
You can obtain credentials for them by running the following command:

```bash
adhar get secrets
```

<details>
  <summary> The "get secrets" command </summary>

  The `get secrets` command retrieves the following:
  - ArgoCD initial admin password.
  - Gitea admin user credentials.
  -  Any secrets labeled with `adhar.io/cli-secret=true`.

  You can think of the command as executing the following kubectl commands:

  ```bash
  kubectl -n argocd get secret argocd-initial-admin-secret
  kubectl get secrets -n gitea gitea-admin-secret
  kubectl get secrets -A -l adhar.io/cli-secret=true
  ```
  In addition, secrets labeled with `adhar.io/package-name` can be specified with the `-p` flag. For example, for Gitea:

  ```bash
  adhar get secrets -p gitea
  ```

</details>

## Contribution Guide :mortar_board:

Learn about integrating, deploying and managing your apps on Adhar platform.

- [Architecture overview](./docs/architecture.md)
- [Setting up your environment](./docs/setup.md)
- [Development guide](./docs/development.md)


## Platform Architecture :crystal_ball:

<p align="center"><img src="docs/imgs/adhar-platform.svg" width="100%" alt="Adhar platform"></p>

## Platform Components

### Adhar Console (Self Service Portal)

Adhar Console stands as the centerpiece of our platform, offering a seamless user experience tailored for both developers and platform administrators. It serves as a one-stop solution for a multitude of tasks. Developers can leverage the Adhar Console to build images, deploy applications, expose services, configure CNAMEs, manage network policies, and handle secrets.

On the other hand, platform administrators can use it to enable and configure platform capabilities, as well as onboard development teams. The Adhar Console goes beyond just a web-based self-service portal, providing direct and context-aware access to platform capabilities like code repositories, registries, logs, metrics, traces, and dashboards.

Moreover, it includes a Cloud Shell feature, allowing both developers and admins to run CLI commands. In essence, the Adhar Console is a comprehensive tool designed to streamline and simplify the management of your platform.

![Adhar Console](docs/imgs/adhar-console.png)

### Command Line Interface (CLI)

The Adhar Command Line Interface (CLI) provides a powerful tool for developers and administrators to interact with the Adhar platform, enabling them to manage resources, execute tasks, and automate workflows directly from the command line.

![Adhar Console](docs/imgs/adhar-cli.png)

### Adhar Control plane (api-server)

In the Adhar platform, the api-server plays a crucial role in enabling seamless integration. Every alteration made via the Console is first verified by the api-server within the control plane. Once validated, these changes are stored in the state store. This action initiates an automatic process where the platform aligns the actual state with the desired state, thereby ensuring smooth integration and consistency across the platform.

### Adhar AI

Adhar AI is an innovative feature of the Adhar platform that integrates AI assistance into your workflow. It leverages advanced machine learning algorithms to provide intelligent recommendations, automate routine tasks, and enhance decision-making processes. Whether you're configuring your platform, troubleshooting issues, or optimizing performance, Adhar Assist is there to guide you. It learns from your platform's data and usage patterns, continually improving its assistance over time. With Adhar AI, you get a smart companion that helps you make the most of the Adhar platform. As a developer, you will enjoy the assistance provided by `Adhar AI` during development.

### Git Based Infrastructure

Upon installation of Adhar, the desired state of the platform is captured and preserved in the Git repository, specifically within the `adhar/values` repository in the Gitea. This Git-based state store plays a pivotal role in infrastructure management, serving as a reliable and version-controlled source of truth. Any modifications made through the Console are promptly mirrored in this repository. This approach not only ensures consistency and traceability but also facilitates collaboration and rollback capabilities, underscoring the importance of a Git-based state store in modern infrastructure.

### Golden Templates Catalog

The `adhar-io/adhar-templates` Git repository houses a collection of built-in Helm charts, which serve as the backbone for creating workloads within the Console. These charts are designed as golden templates, adhering to trending technology standards, ensuring optimal performance and compatibility. In addition to the built-in charts, the platform also offers the flexibility to add custom charts. This allows users to tailor their workloads to specific needs while maintaining the benefits of standardization. Thus, the `adhar-io/adhar-templates` repository is not just a resource, but a gateway to efficient and standardized workload management on the Adhar platform.

### Automation & Self Service

Automation and self-service capabilities are crucial aspects of modern platforms like Adhar. Automation helps in maintaining consistency, reducing human error, and increasing efficiency by automating repetitive tasks. For instance, it can be used to synchronize the desired state with the actual state of applications, ensuring they are always in sync and reducing the need for manual intervention. On the other hand, self-service capabilities empower users by giving them direct control over their services. This not only improves user satisfaction by providing immediate access to services but also reduces the load on support teams. In the context of Adhar, a platform that serves over a billion users, these features are not just beneficial, they are essential for scalability and user satisfaction.

## Platform Capabilities

The platform offers a set of Kubernetes applications for all the required capabilities. Core applications are always installed, optional applications can be activated. When an application is activated, the application will be installed based on default configuration. Default configuration can be adjusted using the Console.

**Integrated Applications:**

- [Kubernetes](https://github.com/kubernetes/kubernetes): Production-Grade Container Scheduling and Management platform
- [Cilium](https://github.com/cilium/cilium): eBPF-based Networking, Security, and Observability for Kubernetes
- [Keycloak](https://github.com/keycloak/keycloak): Identity and access management for modern applications and services
- [Cert Manager](https://github.com/cert-manager/cert-manager) - Bring your own wildcard certificate or request one from Let's Encrypt
- [Nginx Ingress Controller](https://github.com/kubernetes/ingress-nginx): Ingress controller for Kubernetes
- [External DNS](https://github.com/kubernetes-sigs/external-dns): Synchronize exposed ingresses with DNS providers
- [Argo CD](https://github.com/argoproj/argo-cd): Declarative continuous deployment
- [Kaniko](https://github.com/GoogleContainerTools/kaniko): Build container images from a Dockerfile
- [Paketo build packs](https://github.com/paketo-buildpacks): Cloud Native Buildpack implementations for popular programming language ecosystems
- [Cloudnative-pg](https://github.com/cloudnative-pg/cloudnative-pg): Open source operator designed to manage PostgreSQL workloads
- [Argo Workflows](https://github.com/argoproj/argo-workflows): Open source container-native workflow engine for orchestrating parallel jobs on Kubernetes
- [Argo Events](https://github.com/argoproj/argo-events): Argo Events is an event-driven workflow automation framework for Kubernetes
- [Argo Rollouts](https://github.com/argoproj/argo-rollouts): Provide advanced deployment capabilities such as blue-green, canary, canary analysis, experimentation, and progressive delivery features to Kubernetes
- [Gitea](https://github.com/go-gitea/gitea): Self-hosted Git service
- [Velero](https://github.com/vmware-tanzu/velero): Back up and restore your Kubernetes cluster resources and persistent volumes
- [Knative](https://github.com/knative/serving): Deploy and manage serverless workloads
- [Prometheus](https://github.com/prometheus/prometheus): Collecting container application metrics
- [Grafana](https://github.com/grafana/grafana): Visualize metrics, logs, and traces from multiple sources
- [Grafana Loki](https://github.com/grafana/loki): Collecting container application logs
- [Grafana Tempo](https://github.com/grafana/tempo): High-scale distributed tracing backend
- [Harbor](https://github.com/goharbor/harbor): Container image registry with role-based access control, image scanning, and image signing
- [HashiCorp Vault](https://github.com/hashicorp/vault): Manage Secrets and Protect Sensitive Data
- [Kyverno](https://github.com/kyverno/kyverno): Cloud Native Policy Management for Kubernetes
- [Jaeger](https://github.com/jaegertracing/jaeger): End-to-end distributed tracing and monitor for complex distributed systems
- [Backstage](https://github.com/backstage/backstage): Open source framework for building developer portals
- [Minio](https://github.com/minio/minio): High performance Object Storage compatible with Amazon S3 cloud storage service
- [Trivy](https://github.com/aquasecurity/trivy-operator): Kubernetes-native security toolkit
- [Falco](https://github.com/falcosecurity/falco): Cloud Native Runtime Security
- [Crossplane](https://github.com/crossplane/crossplane): A framework for building cloud native control planes
- [Headlamp](https://github.com/headlamp-k8s/headlamp): A Kubernetes web UI that is fully-featured, user-friendly and extensible
- [OpenTelemetry](https://github.com/open-telemetry/opentelemetry-operator): Instrument, generate, collect, and export telemetry data to help you analyze your software’s performance and behavior

### Supported providers :cloud:

Adhar Platform can be installed on any Kubernetes cluster. At this time, the following providers are supported:

- `aws` for [AWS Elastic Kubernetes Service](https://aws.amazon.com/eks/)
- `azure` for [Azure Kubernetes Service](https://azure.microsoft.com/en-us/products/kubernetes-service)
- `google` for [Google Kubernetes Engine](https://cloud.google.com/kubernetes-engine?hl=en)
- `digitalocean` for [DigitalOcean Kubernetes](https://www.digitalocean.com/)
- `civo` for [Civo Cloud K3S](https://www.civo.com/)
- `custom` for any other cloud/infrastructure

## How Adhar Platform Helps :rocket:

:nail_care:**Design Team** - AI-powered design assistance and realistic content generation are rapidly transforming the creative process, making it easier and more efficient for designers and developers to create high-quality products. Additionally, easy design-to-code and code-to-design sync tools are helping to bridge the gap between design and development, ensuring that designs are implemented accurately and efficiently.

- Use the Figma design components to build the application design
- AI can generate the initial DRAFT version of the design and export to Figma
- Designers love Figma, use it with full freedom
- Export all visual elements as design-tokens which developers consume directly
- Keep the sync between the design and the actual application code
- Immidiate feedback look, make the change in design, see it in actual application immidiately
- Don't have to depend on Developers for any change, be involve with the proccess
- Evolve your design as long as it doesn't break the contract
- Easy Collaborate with business team, tech team on same platform

:moneybag:**Business Team** - Business team involvement in transparent collaboration with the Tech team is crucial for designing and developing software products that align with business objectives and meet user needs. By providing wireframes, prototypes, and detailed business requirements directly in the system used by the Tech team, businesses can ensure seamless communication and alignment throughout the development process.

- Create the prototypes which are part of real application, no through away effort after handing over to developers
- Involve in improving the user journey through out the lifecycle
- All the tools at your finger tip to create awesome journeys
- Don't depend on developers to recreate the jourenies based on your input, you take the ownership
- Developers will help to take it forward to next stages of the lifecycle
- Always on top of the realistic status for various features, don't have to ask tech team
- Improve UI/UX by providing feedback to design team in same system
- Improve functionality and app performance by giving feedback to development team
- All the analytics in same platform for making any business decission after delivery
- Discover the insights and improve the product incrementaly

:computer: **Application Team** - Easy self-service is a crucial aspect of enabling developers and operators to focus on their core responsibilities and reduce the burden of managing infrastructure and resources. By providing self-service capabilities, organizations can empower their technical teams.

- Scan source code for vulnerabilities
- Build OCI compliant images from application code and store them in a private registry
- Deploy containerized workloads using a developer catalog with build-in or BYO golden path templates
- Automatically update container images of workloads
- Publicly expose applications
- Get instant access to logs, metrics and traces
- Store charts and images in a private registry
- Configure network policies, response headers and CNAMEs
- Manage secrets
- Create private Git repositories and custom CI/CD pipelines

:battery: **Platform Team** - Platform engineers play a critical role in enabling developers to build, deploy, and manage applications effectively. By building and managing a Kubernetes-based platform, platform engineers provide developers with a self-service platform that simplifies and streamlines the process of bringing applications to production.

- Create your platform profile and deploy to any K8s
- Onboard development teams in a comprehensive multi-tenant setup and make them self-serving
- Get all the required capabilities in an integrated and automated way
- Ensure governance with security policies
- Implement zero-trust networking
- Change the desired state of the platform based on Configuration-as-Code
- Support multi- and hybrid cloud scenarios
- Prevent cloud provider lock-in
- Implement full observability (metrics, logs, traces, alerts)
- Create Golden path templates and offer them to teams on the platform through a catalog

:cop: **Management Team** - It is important for the management team to have access to all the insights and reports to make the best possible business or technical decisions based on actual data points. This is because data can help to identify trends, patterns, and opportunities that would otherwise be invisible. It can also help to track progress, measure performance, and make informed decisions about resource allocation and strategy.

- Fully integrated analytics platform
- AI powered automated insights and recomendations
- Various reports showcasing different aspects of the application
- Realtime insights with zero filter
- Usage statistics and projection
- Easy Technology and Risk goverance
- Visibility of end to end process

## Contribution :pray:

If you wish to contribute please read our [Contributor Code of Conduct](https://adhar.io/community/code-of-conduct) and [Contribution Guidelines](https://adhar.io/community/get-involved).

If you want to say **thank you** or/and support the active development of Adhar:

- [Star](https://github.com/adhar-io/adhar) the Adhar project on Github
- Feel free to write articles about the project on [dev.to](https://dev.to/), [medium](https://medium.com/) or on your personal blog and share your experiences

This project exists thanks to all the people who have contributed

<a href="https://github.com/adhar-io/adhar/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=adhar-io/adhar" />
</a>

## License :snowflake:

Adhar is licensed under the [Apache 2.0 License](https://github.com/adhar-io/adhar/blob/main/LICENSE).
