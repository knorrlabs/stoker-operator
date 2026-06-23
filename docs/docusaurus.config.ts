import { themes as prismThemes } from "prism-react-renderer";
import type { Config } from "@docusaurus/types";
import type * as Preset from "@docusaurus/preset-classic";

const config: Config = {
  title: "Stoker",
  tagline: "Git-driven configuration sync for Ignition SCADA gateways",
  favicon: "img/logo.png",

  url: "https://ia-eknorr.github.io",
  baseUrl: "/stoker-operator/",

  organizationName: "knorrlabs",
  projectName: "stoker-operator",

  future: {
    v4: true,
  },

  onBrokenLinks: "throw",
  trailingSlash: false,

  markdown: {
    mermaid: true,
    hooks: {
      onBrokenMarkdownLinks: "warn",
    },
    mdx1Compat: {
      admonitions: true,
    },
  },

  themes: [
    "@docusaurus/theme-mermaid",
    [
      require.resolve("@easyops-cn/docusaurus-search-local"),
      { hashed: true, indexBlog: false, docsRouteBasePath: "/" },
    ],
  ],

  plugins: [
    "@docusaurus/plugin-ideal-image",
    "docusaurus-plugin-image-zoom",
    [
      "@docusaurus/plugin-client-redirects",
      {
        // Preserve URLs for pages that have moved. Client-side redirects, since
        // GitHub Pages cannot serve true HTTP 301s.
        redirects: [
          { from: "/guides/upgrading", to: "/upgrading" },
        ],
      },
    ],
    [
      "@signalwire/docusaurus-plugin-llms-txt",
      {
        siteTitle: "Stoker",
        siteDescription: "Git-driven configuration sync for Ignition SCADA gateways",
        depth: 2,
        content: {
          enableLlmsFullTxt: true,
        },
      },
    ],
  ],

  i18n: {
    defaultLocale: "en",
    locales: ["en"],
  },

  presets: [
    [
      "classic",
      {
        docs: {
          routeBasePath: "/",
          sidebarPath: "./sidebars.ts",
          editUrl:
            "https://github.com/knorrlabs/stoker-operator/tree/main/docs/",
        },
        blog: false,
        theme: {
          customCss: "./src/css/custom.css",
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    image: "img/logo.png",
    navbar: {
      title: "Stoker",
      logo: {
        alt: "Stoker Logo",
        src: "img/logo.png",
      },
      items: [
        {
          type: "docSidebar",
          sidebarId: "docs",
          position: "left",
          label: "Docs",
        },
        {
          href: "https://github.com/knorrlabs/stoker-operator",
          label: "GitHub",
          position: "right",
        },
      ],
    },
    footer: {
      style: "dark",
      links: [
        {
          title: "Docs",
          items: [
            { label: "Quickstart", to: "/quickstart" },
            { label: "Installation", to: "/installation" },
            { label: "Helm Values", to: "/reference/helm-values" },
          ],
        },
        {
          title: "More",
          items: [
            {
              label: "GitHub",
              href: "https://github.com/knorrlabs/stoker-operator",
            },
            {
              label: "Changelog",
              href: "https://github.com/knorrlabs/stoker-operator/blob/main/CHANGELOG.md",
            },
            {
              label: "Helm Chart",
              href: "https://github.com/knorrlabs/stoker-operator/tree/main/charts/stoker-operator",
            },
            {
              label: "Ignition Helm Chart",
              href: "https://charts.ia.io",
            },
          ],
        },
      ],
      copyright: `Copyright ${new Date().getFullYear()} Stoker Contributors. MIT License.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ["bash", "yaml"],
    },
    colorMode: {
      defaultMode: "light",
      respectPrefersColorScheme: true,
    },
    zoom: {
      selector: ".markdown img",
      background: {
        light: "rgb(255, 255, 255)",
        dark: "rgb(50, 50, 50)",
      },
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
