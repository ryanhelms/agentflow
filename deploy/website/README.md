# Deploy the Agentflow launch site

The website serves a static build. The deployment uses an Agentflow-owned hostname and the `agentflow` namespace. Environment-specific registry, Gateway, DNS, IAP, and reconciliation settings belong to the approved GitOps overlay.

## Build an image

1. Verify the first signed Agentflow release and the GitHub repository cutover. Configure the `website-publish` GitHub environment and its reviewers. Set `AGENTFLOW_SITE_WIF_PROVIDER`, `AGENTFLOW_SITE_PUBLISHER`, and `AGENTFLOW_SITE_IMAGE` as environment variables. The publisher needs write access to its Artifact Registry repository; the workload needs read access through the cluster's approved image-pull identity.
2. Dispatch **Agentflow website** on `main` with the published tag. The workflow verifies the tag, installer checksums and provenance, then publishes the website image and records its immutable digest. Review the image, release revision, and docs source revision together.
3. Keep the production digest in the deployment PR. For local preview without release downloads, build from the repository root with `docker build -f website/Dockerfile.production -t agentflow-website:preview .`.

## Open a restricted preview

1. Create a deployment overlay based on `overlays/gke-example` in the approved GitOps path. Replace the sample image, digest, hostname, HTTPS Gateway reference, and allowed namespace labels with the approved values. Render it with `kustomize build PATH`. The example values deliberately do not identify a live deployment.
2. Configure HTTPS, DNS, and Identity-Aware Proxy (IAP) for the approved pilot access group. Attach the reviewed overlay through Flux. Check pod readiness, the Gateway backend health check, signed-in access, and denial for an unauthenticated browser and an account outside the pilot group. Verify the service is ClusterIP and site traffic enters through the protected route.
3. Run the repo-owner setup and engineer workstation setup pilot, then record a Jira issue through a verified sandbox release and reviewed Confluence update. Verify mobile and keyboard navigation, docs links, copy commands, and screenshot approval. Widen the IAP audience to Nscale Engineering after Engineering Enablement signs off.

## Roll back or move hosting

Revert the GitOps promotion PR to restore the previous image digest and access group, then verify reconciliation, health, and permission behavior. When the dedicated project is ready, deploy the same digest to its overlay, verify access and health, switch traffic at the same hostname, and retain the old stack through the rollback window.
