import clsx from "clsx";
import Link from "@docusaurus/Link";
import useDocusaurusContext from "@docusaurus/useDocusaurusContext";
import Layout from "@theme/Layout";

const features = [
  {
    title: "Git-Driven Sync",
    description:
      "Manage Ignition gateway projects, tags, and resources in Git. Stoker continuously syncs configuration to your gateways.",
    to: "/overview/concepts",
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.75} strokeLinecap="round" strokeLinejoin="round">
        <circle cx="12" cy="12" r="3" />
        <line x1="3" y1="12" x2="9" y2="12" />
        <line x1="15" y1="12" x2="21" y2="12" />
        <polyline points="18 9 21 12 18 15" />
        <polyline points="6 9 3 12 6 15" />
      </svg>
    ),
  },
  {
    title: "Multi-Gateway Support",
    description:
      "Manage any number of gateways from a single repository. Template variables route the right config to the right gateway automatically.",
    to: "/guides/multi-gateway",
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.75} strokeLinecap="round" strokeLinejoin="round">
        <rect x="2" y="3" width="6" height="5" rx="1" />
        <rect x="9" y="3" width="6" height="5" rx="1" />
        <rect x="16" y="3" width="6" height="5" rx="1" />
        <rect x="5" y="16" width="14" height="5" rx="1" />
        <line x1="5" y1="8" x2="5" y2="13" />
        <line x1="12" y1="8" x2="12" y2="13" />
        <line x1="19" y1="8" x2="19" y2="13" />
        <line x1="5" y1="13" x2="19" y2="13" />
        <line x1="12" y1="13" x2="12" y2="16" />
      </svg>
    ),
  },
  {
    title: "Automatic Sidecar Injection",
    description:
      "A MutatingWebhook injects the sync agent into annotated pods. Just add an annotation and Stoker handles the rest.",
    to: "/overview/architecture",
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.75} strokeLinecap="round" strokeLinejoin="round">
        <path d="M12 2L2 7l10 5 10-5-10-5z" />
        <path d="M2 17l10 5 10-5" />
        <path d="M2 12l10 5 10-5" />
      </svg>
    ),
  },
];

const quickLinks = [
  { label: "Quickstart", to: "/quickstart", description: "Up and running in minutes" },
  { label: "Installation", to: "/installation", description: "Helm chart and configuration" },
  { label: "GatewaySync CR", to: "/reference/gatewaysync-cr", description: "Full API reference" },
  { label: "Changelog", to: "https://github.com/ia-eknorr/stoker-operator/blob/main/CHANGELOG.md", description: "What's new in each release" },
];

function Hero() {
  const { siteConfig } = useDocusaurusContext();
  return (
    <header className="hero hero--primary">
      <div className="container">
        <img
          src="img/logo.png"
          alt="Stoker logo"
          width="140"
          style={{ marginBottom: "1rem" }}
        />
        <h1 className="hero__title">{siteConfig.title}</h1>
        <p className="hero__subtitle">{siteConfig.tagline}</p>
        <div style={{ marginTop: "1.5rem" }}>
          <Link className="button button--secondary button--lg" to="/quickstart">
            Get Started
          </Link>
        </div>
      </div>
    </header>
  );
}

function Features() {
  return (
    <section className="features">
      <div className="container">
        <div className="row">
          {features.map((f, idx) => (
            <div key={idx} className={clsx("col col--4")}>
              <Link to={f.to} className="feature feature--link">
                <div className="feature__icon">{f.icon}</div>
                <h3>{f.title}</h3>
                <p>{f.description}</p>
                <span className="feature__arrow">Learn more →</span>
              </Link>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}

function QuickLinks() {
  return (
    <section className="quick-links">
      <div className="container">
        <h2 className="quick-links__heading">Jump In</h2>
        <div className="row">
          {quickLinks.map((link, idx) => (
            <div key={idx} className="col col--3">
              <Link to={link.to} className="quick-link">
                <span className="quick-link__label">{link.label}</span>
                <span className="quick-link__desc">{link.description}</span>
              </Link>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}

export default function Home(): JSX.Element {
  const { siteConfig } = useDocusaurusContext();
  return (
    <Layout title={siteConfig.title} description={siteConfig.tagline}>
      <Hero />
      <main>
        <Features />
        <QuickLinks />
      </main>
    </Layout>
  );
}
