# Comparação de frontends e formatos (1.5)

Este registro documenta uma comparação preliminar, documental e não executada
de Tree-sitter, SCIP e Joern para a fronteira de frontends do Manu. Foi
atualizado em 23/08/2026 a partir das fontes oficiais listadas ao final, do
manifesto do corpus e das restrições do Agent. A comparação é uma decisão
documental de não promover uma dependência ao núcleo; não é um benchmark de
desempenho ou de cobertura executado.

## Escopo e evidência disponível

O corte pretendido precisa cobrir Java/Quarkus, WSO2/CAR e Python/Frappe com
profundidade declarada. O [manifesto canônico do corpus](../evaluation/first-vertical-slice-corpus.md)
fixa Ticketmaster com 61 fontes Java principais, uma amostra de seis CARs de
CarbonApps e ERPNext com 5.316 arquivos rastreados, dos quais 2.940 são
Python. A linha de base Go existente exercita esse recorte, mas não mede os
três candidatos desta comparação.

No estado local observado, `testdata/` tem 68K e contém apenas fixtures
pequenas de Java, XML e texto; não há uma fixture Python/Frappe ou um CAR
persistido. O CAR dos testes é criado em memória, conforme
[`testdata/analyzers/README.md`](../../testdata/analyzers/README.md), e não
substitui o corpus externo autorizado.

Também não havia `tree-sitter`, `scip` ou `joern` disponível no `PATH` durante
esta verificação. Portanto, a coluna de corpus abaixo registra a cobertura que
as fontes oficiais permitem esperar e as lacunas que ainda exigem execução
controlada. Nenhum resultado é apresentado como recall, precisão, tamanho de
binário ou equivalência semântica medida.

## Restrições do Agent

O perfil padrão do Agent é local, determinístico, sem rede, sem execução da
fonte, sem instalação de dependências da fonte e compatível com
`CGO_ENABLED=0` nos builds Linux amd64 e arm64. O bundle e os locadores devem
ser limitados, versionados e verificáveis; a fonte original não deve ser
transportada por padrão. Um frontend externo pode existir em perfil isolado ou
produzir um índice importável, mas não pode ampliar o acesso do processo nem
substituir o kernel factual.

## Matriz de comparação

| Critério | Tree-sitter | SCIP | Joern |
| --- | --- | --- | --- |
| Papel declarado | Gerador de parser e biblioteca de parsing incremental; produz árvore sintática concreta. | Protocolo agnóstico de linguagem para índices de navegação; não é um parser nem um indexador único. | Plataforma de análise que produz Code Property Graphs e consultas para análise estática de código. |
| Cobertura indicada para o corpus | Há parsers oficiais para Java e Python. Isso cobre sintaxe, não semântica de Quarkus/Frappe, relações de build ou o significado de CAR/WSO2. A cobertura de XML e extensões de framework depende de gramáticas e normalizadores adicionais. | O repositório lista indexadores para Java/Scala/Kotlin e Python, entre outras linguagens, com definições/referências como foco. Não há, no protocolo, garantia de semântica de Quarkus, WSO2/CAR, Frappe ou documentos; a cobertura depende de cada indexador e ambiente. | A documentação lista Java e Python, com maturidades declaradas, e oferece relações/CPG para código. O escopo não cobre por si só CAR, configuração operacional ou documentação, e não há uma garantia oficial de cobertura do corpus do Manu. |
| Locadores | A API C expõe nós com posição inicial/final em bytes e pontos de linha/coluna. O frontend ainda precisa converter isso para o locator canônico e preservar revisão e artefato. | O schema carrega caminho relativo canônico, ocorrências, símbolos e encoding de posição; também identifica ferramenta e versão. É o candidato mais direto para intercâmbio de locadores de código, mas não substitui evidência do Manu. | O CPG navega por arquivos, métodos, chamadas e outras construções. A forma de transportar arquivo, posição e revisão para o locator do Manu ainda exige um adaptador e teste no corpus; o CPG não é aceito como contrato público. |
| Determinismo | O runtime local e a árvore são compatíveis com o perfil, mas a documentação consultada não fornece uma garantia de digest do Manu. Gramáticas, versão, entrada e ordenação devem ser fixadas e testadas. | O protocolo serializa a identidade e os dados do índice, mas o determinismo da saída é responsabilidade do indexador. A documentação de autoria recomenda testar resultados determinísticos; isso ainda não foi medido neste corpus. | A plataforma usa frontends, passes, banco de grafo e consultas versionados. A saída precisa de versão, configuração e golden próprios; não há evidência de equivalência determinística com o kernel do Manu. |
| Licença | Runtime oficial MIT; as gramáticas devem ser verificadas individualmente. Os repositórios oficiais de Java e Python exibem MIT. | Repositório e schema Apache-2.0; cada indexador conserva sua própria licença e dependências. | Repositório Apache-2.0; plugins, frontends e dependências ainda exigem inventário próprio. |
| Tamanho e operação | O runtime é descrito como C11 puro e sem dependências; não foi medido o tamanho do runtime, das gramáticas ou do binding Go. | O schema é compacto como contrato e o CLI oficial pode ser compilado com Go, mas o próprio schema alerta para payloads com grande uso de memória e recomenda consumo em streaming; tamanho dos índices e indexadores não foi medido. | A instalação oficial requer JDK 19; o build usa sbt e a documentação alerta para consumo de heap e processos JVM adicionais. A página oficial de releases lista artefatos Linux de 1,69 GB na versão consultada. |
| Isolamento | Pode ser usado como biblioteca de parsing, sem executar a fonte ou acessar rede por si só. Ainda precisa de limites de entrada e validação do código/gramática incorporado ao processo. | O consumo de um arquivo SCIP pode ocorrer no perfil `imported-index`; a produção pode depender de compilador, language server, Node, JVM ou ambiente de build do indexador. | Deve ser tratado como ferramenta externa isolada: a plataforma oferece REPL/DSL, plugins com acesso a filesystem/rede e execução em JVM. Não cabe no perfil `safe-static`. |
| Build estático do Agent | O runtime C pode ser compilado estaticamente em princípio, mas a compatibilidade do binding e do conjunto de gramáticas com `CGO_ENABLED=0` não foi demonstrada; requer spike separado. | O CLI de referência tem caminho de build Go, mas isso não torna os indexadores ou bindings usados pelo corpus estáticos. Importar o formato não exige embutir o indexador. | Não atende ao build estático Go do Agent: depende de JVM/sbt ou de uma distribuição JVM grande. Deve permanecer externo, se aprovado em mudança posterior. |

## Decisão desta comparação

Nenhuma das três opções é promovida ao núcleo nesta mudança.

- **Tree-sitter:** permanece candidato a frontend sintático opcional. Só pode
  ser considerado para o Agent depois de medir cobertura e locadores em
  Java/Quarkus, XML/WSO2 e Python/Frappe, repetir o digest, verificar licença
  das gramáticas e provar o build estático escolhido.
- **SCIP:** permanece candidato a formato de intercâmbio no perfil
  `imported-index`, sujeito à validação de escopo, revisão, locadores,
  produtor, versão, limites e licenças dos indexadores. O protocolo não se
  torna o contrato semântico do `Knowledge Engine`.
- **Joern:** permanece candidato a produtor semântico em perfil
  `semantic-isolated`, caso um caso de competência mostre valor que compense
  JVM, distribuição, memória, isolamento e adaptação de locadores. Não será
  incluído no binário principal.

O núcleo continua usando frontends seguros locais e o `Analysis Bundle` como
fronteira de intercâmbio. Um futuro benchmark só poderá alterar esta decisão
com versões fixadas e corpus autorizado, casos de competência, comparação de
predicados e locadores, repetição determinística, medição de memória/bytes/
duração, inventário de licenças, teste de isolamento e verificação dos builds
Linux estáticos. Ausência de resultado em uma família deve permanecer uma
`Explicit Gap`, não ser preenchida por uma ferramenta escolhida por
conveniência.

## Fontes oficiais consultadas

- [Tree-sitter — introdução](https://tree-sitter.github.io/tree-sitter/), [uso de parsers](https://tree-sitter.github.io/tree-sitter/using-parsers/), [API C](https://github.com/tree-sitter/tree-sitter/blob/master/lib/include/tree_sitter/api.h) e [licença do runtime](https://github.com/tree-sitter/tree-sitter/blob/master/LICENSE).
- [Gramática Tree-sitter para Java](https://github.com/tree-sitter/tree-sitter-java) e [gramática Tree-sitter para Python](https://github.com/tree-sitter/tree-sitter-python).
- [SCIP README](https://github.com/scip-code/scip/blob/main/README.md), [schema `scip.proto`](https://github.com/scip-code/scip/blob/main/scip.proto) e [licença](https://github.com/scip-code/scip/blob/main/LICENSE).
- [Joern — visão geral](https://docs.joern.io/), [Code Property Graph](https://docs.joern.io/code-property-graph/), [instalação e build](https://docs.joern.io/installation/), [extensões](https://docs.joern.io/extensions/), [releases](https://github.com/joernio/joern/releases) e [licença](https://github.com/joernio/joern/blob/master/LICENSE).

## Relações

- ADR: [`0005-kernel-factual-frontends-substituiveis-e-intercambio.md`](../decisions/0005-kernel-factual-frontends-substituiveis-e-intercambio.md)
- OpenSpec: [tarefa 1.5](../../openspec/changes/archive/2026-08-30-establish-evidence-backed-context-engine/tasks.md)
