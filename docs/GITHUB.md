# GitHub

## Deploy from a GitHub repository

The **Deploy from GitHub** package runs your own project on the pool, straight from its repository:

1. On the dashboard, open **Marketplace → Deploy from GitHub** (or **Integrations → GitHub**).
2. Enter the repository (`owner/name` or its link), the branch to follow (empty for the default branch) and the port your app listens on. If the Dockerfile isn't at the top of the repository, enter its folder and, if it's not called `Dockerfile`, its name.
3. **Deploy**. The node asks GitHub for the branch's latest commit, and one of your machines builds that commit's Dockerfile and runs it. The port is opened on that machine, and the app gets a `PORT` environment variable.

**New commits deploy themselves.** The node checks the branch every 3 minutes. When it moves, the app is built and started again at the new commit, keeping its data. There's no webhook to set up and no token to hand over: the node only reads public information from GitHub, with conditional requests that don't use up GitHub's rate limit.

**Data.** Files the app writes to `/data` are kept in [pooled storage](STORAGE.md) and follow it across rebuilds and moves to other machines.

**Where it runs.** Building runs the Dockerfile's steps, so these apps run only on your own machines with a container runtime, never on machines lent to you. The image is built on the machine that runs the app, and older builds of the app are removed there.

**Limits for now.** Public repositories on GitHub only. During a rebuild the app is briefly down. Private repositories (with a scoped token) and zero-downtime switches are on the [roadmap](ROADMAP.md).

### In a package

Any container package can be built from a repository instead of pulling an image:

```json
"run": {
  "kind": "container",
  "build": {"repo": "owner/name", "branch": "main", "dir": "web", "dockerfile": "Dockerfile.prod"},
  "ownerOnly": true,
  "network": true,
  "ports": ["8080:8080"]
}
```

`build.commit` is filled in by the node; `ownerOnly` is required.

## Self-hosted Actions runners

The **GitHub Actions runner** package uses spare capacity as a self-hosted runner for your repositories: see its description in the marketplace.
