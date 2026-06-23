import type { SidebarsConfig } from "@docusaurus/plugin-content-docs";

const sidebars: SidebarsConfig = {
  docs: [
    {
      type: "category",
      label: "Overview",
      collapsed: false,
      items: ["overview/introduction", "overview/architecture", "overview/concepts"],
    },
    {
      type: "category",
      label: "Getting Started",
      collapsed: false,
      items: ["quickstart", "installation"],
    },
    {
      type: "category",
      label: "Guides",
      collapsed: false,
      items: [
        "guides/git-authentication",
        "guides/multi-gateway",
        "guides/content-templating",
        "guides/json-patches",
        "guides/webhook-sync",
        "guides/monitoring",
        "guides/multi-site-deployment",
        "guides/upgrading",
      ],
    },
    {
      type: "category",
      label: "Reference",
      collapsed: false,
      items: [
        "reference/gatewaysync-cr",
        "reference/helm-values",
        "reference/annotations",
        "reference/troubleshooting",
      ],
    },
    "roadmap",
  ],
};

export default sidebars;
