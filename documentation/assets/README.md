# Documentation assets

`favicon.svg` is the manual's maintained site icon. [catalog-ui/](catalog-ui/README.md) contains reviewed screenshots illustrating the browser client. [monitoring/](monitoring/README.md) contains Grafana, Prometheus and Jaeger illustrations with their capture procedures.

[contextforge/](contextforge/README.md) contains native upstream and virtual-server screenshots from the isolated integration guide.

The documentation preparation step copies only explicitly registered assets into build-owned public output. Follow the [asset and screenshot guidance](../src/content/docs/contributing/documentation.mdx); do not place raw browser captures, credentials or arbitrary files in the public tree.

[sonarqube/](sonarqube/README.md) contains the completed local analysis illustration and its capture procedure.
