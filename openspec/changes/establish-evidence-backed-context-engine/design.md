## Context

O corte atual já possui descoberta, `Analysis Bundle`, contribuições com identidade de analisador, evidências, cobertura e lacunas, ingestão durável, projeções textuais/vetoriais/relacionais e composição limitada de `Evidence Package`. Entretanto, `Contribution.Value` ainda concentra payloads privados dos analisadores, Java usa reconhecimento lexical limitado, WSO2 produz fatos declarativos próprios e Python/Frappe recebe principalmente inventário e texto. A recuperação existente foi desenhada para a consulta assistida e sua primeira linha de base observou recall insuficiente; portanto, adicionar uma superfície MCP antes de melhorar fatos e recuperação apenas tornaria a deficiência acessível por outro protocolo.

O Agent deve continuar local, determinístico, executável sem banco ou IA e compatível com builds Linux estáticos. PostgreSQL permanece a fonte de verdade da célula e suas projeções continuam reconstruíveis. A autorização para analisar uma fonte permanece separada da autorização para expor conteúdo a um modelo. Veja `proposal.md` para a motivação e as delta specs para os comportamentos exigidos.

## Goals / Non-Goals

**Goals:**

- criar um substrato factual pequeno e extensível que não precise conhecer todas as linguagens;
- preservar contribuições e bundles atuais durante uma migração aditiva;
- permitir frontends internos e importadores externos atrás do mesmo contrato validado;
- derivar relações universais de forma determinística e rastreável;
- generalizar o pacote atual do gerador em um pacote de contexto neutro para pessoas, API, MCP e `AI Gateway`;
- fornecer uma superfície MCP local pequena, somente leitura e limitada por orçamento;
- validar valor por custo de tarefa correta, não apenas por volume indexado ou tokens reduzidos.

**Non-Goals:**

- provar compreensão completa ou equivalência semântica entre todas as linguagens;
- embarcar Joern, CodeQL, Ghidra, compiladores ou runtimes de linguagem no binário principal;
- acompanhar manualmente toda versão histórica de vinte linguagens neste corte;
- executar build, dependências ou código da fonte no perfil padrão;
- introduzir telemetria de runtime, edição por MCP, SQL/Cypher livre, transporte MCP remoto ou autenticação de produção;
- substituir o código original pela base derivada ou autorizar uma alteração sem reinspeção da fonte;
- anunciar uma redução comercial de tokens antes dos benchmarks previstos.

## Decisions

### 1. Adotar um kernel de fatos universais, não um parser universal

O pipeline será separado em quatro estágios conceituais:

```text
Artifact
  -> Analyzer Contribution
  -> Canonical Fact
  -> Derived Fact
  -> Evidence Unit / projections / Context Package
```

`Contribution` continua sendo a observação bruta e versionada de um frontend. Um normalizador produz `Canonical Fact` somente quando existe mapeamento sustentado; propriedades sem mapeamento seguro permanecem como extensão específica. O fato canônico terá, conceitualmente:

```text
identity
organization / source / snapshot
predicate
subject
object or typed value
qualifiers
producer identity / version / method
evidence identities and locators
input fact identities and rule version, when derived
```

Os predicados iniciais serão restritos a artefato, símbolo ou elemento nomeado, definição, referência, chamada, dependência, configuração, endpoint, mensagem e pertencimento. O vocabulário será aditivo e versionado. Não haverá campo escalar universal de confiança: origem, método, comportamento, cobertura, temporalidade e contestação permanecem qualificadores independentes.

Para um snapshot `s`, as contribuições normalizadas formam:

```text
F_s = union normalize_i(contributions_i(s))
```

As identidades incluem o snapshot e uma representação canônica do predicado, participantes, qualificadores e produtor. A ordenação canônica elimina variação de maps ou concorrência, mas fatos equivalentes de produtores diferentes permanecem contribuições distintas e correlacionáveis.

**Alternativas consideradas:**

- ampliar `Contribution.Value` indefinidamente: preserva compatibilidade imediata, mas obriga cada consumidor a entender todos os formatos privados;
- usar diretamente um Code Property Graph como modelo completo: cobre bem código, mas não documentos, configuração operacional, curadoria, temporalidade e processos de negócio;
- transformar saída da LLM em fato canônico: aumenta cobertura aparente, mas rompe a separação entre observado e gerado.

### 2. Usar o Analysis Bundle como fronteira versionada para frontends

O Agent manterá frontends seguros em processo para o fallback e os recortes já suportados. O `Analysis Bundle` será ampliado aditivamente para `v1alpha2`, mantendo `contract_version=v1alpha1`, leitura dos bundles atuais e o digest factual `v1alpha1` byte a byte compatível. O novo envelope carregará sequências canônicas separadas para manifestos de frontend, fatos e extensões; o digest `v1alpha2` usará separação de domínio própria e incluirá essas sequências ordenadas. Ferramentas externas ou workers isolados não terão ABI de plugin Go: produzirão uma seção ou bundle de intercâmbio validado pelo mesmo ingestidor.

O manifesto de frontend declarará tipos de fonte, famílias e versões reconhecidas, predicados possíveis, dimensões, perfil de execução e identidade exata da ferramenta. Extensões declararão schema por identidade, versão e digest verificável; validade sintática de JSON, isoladamente, não será suficiente para aceitá-las. Adaptadores futuros poderão importar SCIP para símbolos e referências, SBOM para pacotes ou uma projeção de CPG para fluxo; esses formatos nunca se tornarão o contrato público da base.

O primeiro corte migrará Java e WSO2 para normalizadores explícitos e acrescentará um frontend estrutural determinístico para Python/Frappe. Antes de escolher uma biblioteca universal opcional, um spike comparará ao menos Tree-sitter, SCIP e Joern quanto a cobertura do corpus, qualidade de locadores, determinismo, custo, licença, tamanho, isolamento e compatibilidade com o Agent estático. A escolha pode resultar em nenhum deles no binário principal; o protocolo de importação é a proteção contra esse acoplamento.

Perfis de execução:

- `safe-static`: padrão, sem rede, build, instalação ou execução da fonte;
- `semantic-isolated`: opcional, autorizado e limitado, para compiladores ou indexadores externos;
- `imported-index`: valida um índice produzido previamente sem executar a ferramenta produtora.

**Alternativas consideradas:**

- implementar um plugin por linguagem dentro do binário: simples no início, mas acopla ciclos de release e dependências incompatíveis;
- iniciar com um worker Joern obrigatório: oferece CPG e dataflow, mas adiciona runtime JVM, imagem grande e maturidade desigual por linguagem;
- embutir Tree-sitter via CGO: amplia parsing, mas conflita com a distribuição estática atual e não fornece semântica de tipos por si só.

### 3. Derivar relações por regras monotônicas e linhagem explícita

Uma porta de derivação receberá fatos ordenados e regras identificadas por nome e versão. O mecanismo inicial usará regras Go registradas, mas obedecerá semântica monotônica: cada iteração apenas acrescenta fatos novos até atingir o menor ponto fixo ou um limite controlado.

```text
D_s = lfp(T_R, F_s)
```

Uma fila ordenada, identidade canônica e deduplicação tornam o resultado determinístico. Limites de iterações, fatos e fanout evitam explosão; atingir um limite produz cobertura incompleta e lacuna, não uma relação silenciosamente truncada. Cada fato derivado mantém os fatos de entrada e a versão da regra, possibilitando inspeção e rebuild. Uma futura implementação Datalog poderá substituir o executor sem mudar o contrato.

**Alternativas consideradas:**

- persistir apenas relações finais: reduz volume, mas elimina explicabilidade e invalidação precisa;
- adotar Datalog externo neste corte: é alinhado ao modelo matemático, porém adiciona runtime e operação antes de existir corpus suficiente para justificar a dependência;
- usar geração por LLM para completar relações: não oferece determinismo nem garantia de suporte.

### 4. Atualizar por identidade de conteúdo e fanout de linhagem

O pipeline calculará a diferença entre snapshots por identidade e hash de artefato. Fatos observados de artefatos inalterados poderão ser reutilizados quando frontend, configuração e schema também forem idênticos. Mudanças invalidarão fatos produzidos pelos artefatos afetados e percorrerão a linhagem reversa das derivações até estabilizar.

O resultado incremental será comparado em testes ao rebuild completo. O objetivo operacional é custo proporcional ao fanout, não a promessa irreal de custo estritamente proporcional apenas aos arquivos alterados. Mudança de versão de frontend, regra ou schema invalida o recorte correspondente mesmo sem mudança na fonte.

**Alternativas consideradas:**

- reanalisar tudo sempre: correto como fallback, mas desperdiça tempo e impede escala;
- invalidar apenas o arquivo modificado: ignora referências, chamadas e dependências derivadas em outros artefatos.

### 5. Criar um Context Package neutro antes do AI Gateway e do MCP

O `Evidence Package` existente é orientado ao `Generator`. Será introduzido um `Context Package` consumidor-neutro acima da recuperação, contendo fatos, entidades, relações, evidências, locadores, cobertura, lacunas, auditoria de seleção e continuidade. Uma projeção sanitizada desse pacote alimentará o `AI Gateway`; outra será serializada pelo MCP. Nenhum consumidor acessará tabelas ou projeções diretamente.

Uma implementação produtiva da porta `ContextService` montará o `Context Package` a partir da leitura canônica escopada no PostgreSQL e das projeções reconstruíveis. O serviço resolverá o snapshot autorizado, adaptará fatos e resultados híbridos para candidatos canônicos, aplicará seleção, fechamento de suporte, orçamento, política e continuação e validará o pacote antes de devolvê-lo. Essa composição não invocará `Generator`; HTTP, MCP e `AI Gateway` continuarão consumidores da mesma porta, sem reconstruir o pipeline em cada adaptador.

O planejamento de contexto parte de uma intenção tipada: pergunta livre, entidade ou símbolo, impacto possível ou inspeção de evidência. A recuperação gera candidatos por sinais exatos, textuais, vetoriais e relacionais. O compositor resolve uma variante determinística de cobertura máxima sob orçamento:

```text
maximize U(S | q)
subject to sum(cost(e)) <= B
           policy(e) = allowed
           scope(e) = requested snapshot
```

`U` combina relevância, cobertura dos aspectos da intenção, diversidade de tipos e artefatos e fechamento relacional útil. A implementação inicial usará seleção gulosa por ganho marginal por token, desempate por identidade canônica e cotas por artefato e tipo. O algoritmo, pesos e estimador de tokens serão versionados e registrados; não será alegada solução ótima. Evidências obrigatórias à sustentação de uma relação entram como pequeno fechamento de suporte ou fazem a relação ser excluída.

O token estimado continua sendo uma aproximação determinística de transporte, distinta da contagem real de cada modelo. Cursors opacos fixam snapshot, filtros, algoritmo, ordenação e política e expiram quando qualquer um desses elementos for incompatível.

**Alternativas consideradas:**

- retornar os primeiros resultados até truncar: barato, mas favorece redundância e pode separar relações de seu suporte;
- deixar a LLM selecionar diretamente todos os arquivos: recria o custo que a capacidade pretende reduzir e dificulta reprodução;
- retornar apenas resumos gerados: economiza conteúdo, mas perde fonte verificável e depende de modelo.

### 6. Expor MCP como adaptador fino e somente leitura

O comando `manu mcp` iniciará um servidor `stdio` usando uma versão estável fixada do SDK Go oficial do MCP e declarará a versão de protocolo suportada. A dependência ficará isolada em um adaptador; tipos MCP não atravessarão a porta da aplicação. As ferramentas receberão a implementação produtiva de `ContextService` composta no limite do processo e não acessarão repositórios ou projeções diretamente. O transporte remoto não será habilitado neste corte.

A superfície inicial terá quatro ferramentas, em ordem determinística:

```text
manu_query
manu_context
manu_impact
manu_evidence
```

`manu_query` recebe pergunta e filtros; `manu_context` recebe entidade ou símbolo; `manu_impact` expande relações sustentadas e qualificadas como possíveis; `manu_evidence` reinspeciona identidades. Respostas usam conteúdo estruturado e, quando útil, links de recurso no namespace `manu://organizations/{organization}/sources/{source}/snapshots/{snapshot}/evidence/{id}`. O recurso é um identificador de aplicação, não caminho de filesystem.

As descrições serão concisas porque schemas de ferramentas também consomem contexto do modelo. Não haverá ferramenta por linguagem, SQL, Cypher, mutação, reindexação ou administração. No modo local atual, a única `Organization` configurada ainda será exigida logicamente; uma futura superfície remota exigirá nova mudança com autenticação e autorização.

**Alternativas consideradas:**

- criar um MCP separado por analisador: expõe detalhes acoplados e aumenta o contexto de descoberta das ferramentas;
- servir o banco diretamente: contorna política, auditoria e estabilidade do contrato;
- começar por Streamable HTTP: amplia superfície de segurança e operação sem necessidade para validar integração local.

### 7. Medir custo por tarefa correta e sustentada

O runner de avaliação será ampliado com variantes controladas:

```text
direct-source     agente usa somente ferramentas de filesystem/busca
text-retrieval    agente usa recuperação textual disponível
manu-context      agente usa o Context Package/MCP
external-context comparação opcional, quando configurada
```

Cada caso fixa revisão, pergunta ou tarefa, critérios de sucesso, evidências esperadas e permissões. Instrumentação registra chamadas, tokens informados pelo provedor, estimativas quando necessário, arquivos e bytes lidos quando observáveis, tempo, custo, recall, precisão, citações e conclusão. Estimativa e medição real nunca serão misturadas.

O indicador principal será custo ou esforço por tarefa correta e sustentada. Economia percentual será derivada somente entre execuções comparáveis:

```text
saving = 1 - cost(manu-context) / cost(baseline)
```

Se a variante não produzir sucesso correto, o custo por sucesso é indefinido. Relatórios preservarão amostra, dispersão e limitações; não haverá extrapolação de um corpus para todas as linguagens.

**Alternativas consideradas:**

- medir apenas tokens do pacote: não captura chamadas de descoberta nem correção;
- medir somente latência: favorece respostas rápidas e erradas;
- exigir GitNexus como baseline: cria dependência externa e não representa todos os ambientes; ele permanece comparador opcional e identificado.

### 8. Registrar a decisão factual e atualizar a identidade do produto

Uma ADR registrará a adoção do kernel de fatos, derivação com linhagem e frontends substituíveis, pois essa fronteira é difícil de reverter. MCP não será tratado como centro nem exigirá ADR própria: é um adaptador substituível sobre o `Context Package`.

`PRODUCT.md` apresentará contexto para agentes como experiência derivada e economia de exploração/tokens como hipótese mensurável. `DOMAIN.md` definirá somente termos conceituais necessários, evitando transformar `Canonical Fact`, algoritmo de ranking ou MCP em linguagem pública se forem detalhes internos. `ARCHITECTURE.md` mostrará os novos estágios, a fronteira do MCP e os perfis de frontend.

## Estado de implementação e retomada

Estado registrado em 23/08/2026 para transferência entre ambientes:

- tarefas `1.1` a `1.5` concluídas e revisadas, incluindo `PRODUCT.md`, `DOMAIN.md`, `ARCHITECTURE.md`, ADR 0005 e a comparação preliminar de frontends;
- progresso OpenSpec em `5/59`; nenhuma tarefa de código foi concluída e `internal/fact` ainda não existe;
- próxima tarefa executável: `2.1`, seguida por `2.2` e `2.3`; bundle e persistência devem aguardar a identidade factual ficar estável;
- o repositório exige implementação delegada a subagentes `implementer` com GPT-5.6 Luna; a conta do ambiente de origem não disponibilizou esse modelo, e o agente principal permaneceu fora da implementação conforme `AGENTS.md`;
- a toolchain Go não estava no `PATH`, e o daemon Docker exigia senha interativa; por isso nenhum teste ou build Go foi alegado neste ponto;
- `openspec validate establish-evidence-backed-context-engine --strict` e `git diff --check` passaram antes do commit de transferência.

Ao retomar, o novo ambiente deve primeiro confirmar GPT-5.6 Luna, Go e, para testes PostgreSQL/Compose, acesso ao Docker. Em seguida deve executar `openspec instructions apply --change establish-evidence-backed-context-engine --json`, iniciar em `2.1` e marcar cada checkbox somente após revisão e verificação. As limitações acima descrevem o ambiente de origem e não são requisitos do produto.

## Risks / Trade-offs

- [O vocabulário universal ficar genérico demais] -> começar com poucos predicados verificáveis, preservar extensões específicas e promover novos conceitos somente com corpus e perguntas de competência.
- [O substrato duplicar `Contribution`, `Observation` e `Relationship`] -> documentar o mapeamento de responsabilidade e manter fatos como representação técnica interna, sem criar sinônimos públicos desnecessários.
- [A derivação crescer exponencialmente] -> limitar fanout, iterações e tipos de regra, registrar truncamento e medir rebuild e atualização incremental.
- [A seleção sob orçamento excluir a evidência decisiva] -> manter auditoria de candidatos, fechamento de suporte, continuação estável e avaliação de recall antes de otimizar redução de tokens.
- [MCP ampliar exfiltração de código] -> somente `stdio`, escopo lógico obrigatório, revalidação por chamada, sem consulta livre, redaction e auditoria das identidades entregues.
- [Descrições ou respostas MCP consumirem mais contexto do que economizam] -> quatro ferramentas estáveis, schemas concisos, orçamento padrão e benchmark incluindo overhead de descoberta e chamadas.
- [Dependências de parsing quebrarem builds estáticos] -> não embutir runtime nativo no núcleo; avaliar ferramentas opcionais e usar bundles/importadores ou workers isolados.
- [Índice ficar desatualizado e orientar edição incorreta] -> incluir snapshot e revisão em toda resposta, invalidar cursors incompatíveis e direcionar o agente à fonte original antes da alteração.
- [Uma métrica positiva virar promessa ampla] -> exigir relatórios delimitados e manter linguagem de hipótese em produto até evidência representativa.
- [A mudança ficar grande demais para revisão] -> implementar em marcos verticais com testes e gates próprios, mantendo cada marco integrável e sem expor MCP antes de fatos, recuperação e autorização estarem validados.

## Migration Plan

1. Registrar a ADR e atualizar documentos canônicos para fixar responsabilidades e limites antes de mudar contratos executáveis.
2. Acrescentar tipos e o envelope `Analysis Bundle v1alpha2`; manter `contract_version=v1alpha1`, leitura, ingestão e digest de bundles `v1alpha1`, e criar fixtures de compatibilidade.
3. Persistir fatos e linhagem em estruturas aditivas, sem remover contribuições, evidências ou projeções existentes; implementar rebuild a partir dos registros canônicos.
4. Normalizar os resultados atuais de Java e WSO2, acrescentar o frontend Python/Frappe e provar conformidade e equivalência incremental no corpus.
5. Introduzir regras derivadas mínimas e projeções relacionais/textuais necessárias, com limites e auditoria.
6. Criar o `Context Package` e adaptar a consulta atual para derivar o pacote do `Generator` a partir dele; manter a API existente compatível.
7. Adicionar as quatro ferramentas MCP `stdio` atrás de configuração explícita e executar testes de protocolo, limites e isolamento.
8. Ampliar o runner, executar as linhas de base e registrar resultados sem converter a primeira medição em SLA.

Rollback operacional desabilita o comando MCP e a nova seleção, mantendo a API atual. Como fatos, regras e projeções são aditivos, suas estruturas podem permanecer sem uso; bundles antigos continuam aceitos. Remoção de dados derivados só ocorre por rebuild ou migração explícita e nunca remove as contribuições e evidências anteriores usadas como fonte de verdade.
