# Formal tooling provenance and licenses

This is an explicitly acquired, locally executed development tool, outside Catalog Go modules, npm dependencies, application images and distributable artifacts. The installer copies no tool source or binary into maintained application assets. It does not widen the exact Rapid test-dependency license exception.

The Go license gate remains unchanged; it does not assess this Java distribution.

The selected [GobraIDE v2.0.5 bundle](https://github.com/viperproject/gobra-ide/releases/tag/v2.0.5) contains the Gobra server JAR, Z3 and an unused Boogie/.NET distribution. `tool.json` pins the archive and extracted members by SHA-256. Only the JAR, Z3 command and Z3 license are installed.

The JAR remains intact, retaining its bundled notices and components even when the selected CLI/backend does not call them. No VS Code extension, Boogie executable, .NET runtime or separate Z3 Java API is launched.

| Component | Reviewed provenance and license boundary |
| --- | --- |
| Gobra | [Pinned source and license](https://github.com/viperproject/gobra/tree/36e39e31360ff6228b4932f7a4402ecf35f53e01): MPL-2.0 with its stated file-level exceptions. |
| ViperServer | [Pinned source](https://github.com/viperproject/viperserver/tree/840fff63278dd655ce5032d4370cda1da3ee92c2), selected transitively by Gobra; MPL-2.0. |
| Silicon | [Pinned source](https://github.com/viperproject/silicon/tree/f9c2989cf6a86869bc90aa404abb8bb8586b72f7), the explicitly selected backend; MPL-2.0. |
| Silver | [Pinned source](https://github.com/viperproject/silver/tree/42fa22f0c31923b0f304039805328288c45979d3), Viper frontend/intermediate language; MPL-2.0. |
| Z3 command | Bundle's `z3/LICENSE.txt`: MIT; executable reports `Z3 version 4.16.0 - 64 bit`. The JAR also contains Java solver bindings; the fixed command-line backend does not select API mode. |
| Java | Separately installed Java 21. Qualification used OpenJDK; its distribution and notices belong to the operator-provided JDK, not the Gobra archive or Catalog. |

The assembly is not uniformly MPL or Apache licensed. Review used the pinned Gobra/ViperServer/Silicon/Silver build definitions and the actual JAR's namespace and notice inventory. Its closure includes Scala compiler/library/reflect/scalap, Kiama, Scala parser combinators, FastParse and Li Haoyi utility/JSON/request libraries, Scallop, ScalaTest support, Scalaz, ANTLR, Akka 2.6/HTTP, Lift/JSON4S/Spray JSON, LSP4J, Guava, Commons libraries, Jansi, SLF4J/Logback, JGraphT and Alloy/Kodkod/SAT support.

These retain their upstream Apache/BSD/MIT/MPL/EPL/LGPL or dual-license terms; local execution does not relicense them as Catalog code. Embedded notices include the Scala components, Scalaz, Alloy/Kodkod, Checker Qual, Commons Codec/IO/Lang/Pool/Text and associated notices. Some upstream assembly rules discard `LICENSE.txt` resources: embedded notices alone are not a complete redistribution license inventory.

The selected use is execution of the unchanged third-party tool on local source, with no linking into or redistribution alongside the Apache-licensed application. This is not approval to redistribute the JAR, solver, JDK, extension or their transitive closure. Any packaging/distribution proposal requires a separate full source/notice/license review of the chosen artifacts; do not infer approval from a passing Go license gate. Keep upstream attribution and the intact JAR/Z3 notices in the ignored installation. A tool refresh must review changed components and license terms as well as rerun checksums, all proof controls and harness regressions.
