## Context

O repositório contém a fundação documental, o contrato universal de compreensão, o corpus e o protocolo do primeiro corte. Esta aplicação estabelece o primeiro módulo e o esqueleto executável, enquanto o pipeline completo continua sendo trabalho das tarefas posteriores. Consulte [proposal.md](proposal.md) para a motivação e [a especificação do runtime](specs/knowledge-engine-runtime/spec.md) para os comportamentos exigidos.

A escolha aceita é Go-first: Go será a linguagem principal do `Manu Agent`, da CLI, do pipeline comum e do backend inicial. Isso não transforma cada analisador especializado em código Go obrigatório. O contrato universal continua sendo a fronteira semântica; uma especialização futura poderá usar outro runtime quando a biblioteca nativa produzir ganho mensurável que compense empacotamento, isolamento e operação adicionais.

Go não publica versões LTS. A política oficial mantém uma versão principal até existirem duas versões principais mais novas. Em 2026-08-17, a versão estável corrente é Go 1.26.6; a implementação deve confirmar novamente a página oficial de downloads antes de fixar o toolchain.

As pré-condições da fundação foram resolvidas antes da criação do módulo: o
caminho canônico confirmado é `github.com/pedrogpaulino/manu`, e a instalação
local validada é `go version go1.26.6 linux/amd64`. A versão estável foi
confirmada na [página oficial de downloads do Go](https://go.dev/dl/).

## Goals / Non-Goals

**Goals:**

- criar uma fundação pequena que já percorra fontes reais e produza resultados verificáveis, em vez de apenas scaffolding;
- manter o processo local leve, determinístico, cancelável e seguro diante de código e arquivos não confiáveis;
- separar descoberta, analisadores e contrato comum para que especializações evoluam sem duplicar o engine;
- estabelecer uma linha de base de tempo, memória, volume e incrementalidade antes de escolher persistência e recuperação;
- entregar um binário e uma imagem Linux adequados ao futuro Agent on-premises.

**Non-Goals:**

- implementar neste incremento banco relacional, pgvector, embeddings, recuperação híbrida, OpenAI ou resposta RAG;
- criar daemon, protocolo remoto, API HTTP, UI, autenticação, fila ou célula Compose completa;
- alcançar semântica profunda de Java/Quarkus, WSO2 ou Python/Frappe;
- criar framework público de plugins ou garantir compatibilidade binária entre analisadores;
- otimizar antes de medir ou introduzir bibliotecas para substituir recursos suficientes da biblioteca padrão.

## Decisions

### 1. Go será o runtime principal, com compatibilidade e atualização explícitas

O módulo `github.com/pedrogpaulino/manu` será construído com o patch estável
mais recente de Go 1.26 verificado no momento da aplicação: Go 1.26.6. Em
17/08/2026, a versão mínima ainda suportada é Go 1.25, conforme a
[política oficial de releases do Go](https://go.dev/doc/devel/release#policy),
e é essa a versão declarada em `go.mod`; a diretiva `toolchain` fixa
`go1.26.6` para a verificação local. A imagem de build também será fixada por
versão, sem tag `latest`, quando a tarefa de distribuição entrar em escopo.

Atualizações de patch entram após testes e benchmark; atualizações de versão principal repetem testes, verificação de vulnerabilidades e a linha de base do corpus antes de alterar o toolchain. Isso traduz “última LTS” para a política real do Go: último patch da linha estável suportada, sem alegar uma modalidade LTS inexistente.

Alternativas consideradas:

- **fixar somente uma versão principal antiga:** amplia a janela de compatibilidade, mas posterga correções e melhorias de runtime sem evidência de necessidade;
- **usar `latest` em builds:** reduz manutenção aparente, porém destrói reprodutibilidade e pode alterar desempenho ou compatibilidade silenciosamente;
- **manter Go, TypeScript e Rust em paralelo:** produziria três fundações antes de validar o motor e multiplicaria a operação do Agent.

### 2. O primeiro projeto será um módulo único e um monólito modular pequeno

O layout inicial será orientado a responsabilidades:

```text
cmd/manu/                 composição e processo da CLI
internal/source/          descoberta, identidade, hashing e leitura segura
internal/contract/        resultado comum e serialização versionada
internal/analysis/        seleção, execução e composição dos analisadores
internal/analyzer/generic/
internal/analyzer/java/
internal/analyzer/wso2/
internal/benchmark/       cenários, métricas e relatórios
testdata/                 fixtures pequenas e não sensíveis
```

Não haverá diretório `pkg`, workspace multi-módulo ou pacote por camada técnica genérica. Dependências serão ligadas por construtores explícitos na raiz de composição; nenhum container de DI será adotado enquanto o grafo permanecer pequeno.

Alternativas consideradas:

- **microserviços ou múltiplos módulos:** antecipam distribuição e versionamento sem existir uma segunda unidade implantável;
- **framework de DI:** ajuda grafos grandes, mas adiciona reflexão, geração ou um container que o primeiro corte não necessita;
- **arquitetura genérica com muitas camadas:** cria interfaces sem consumidores reais e dificulta acompanhar o fluxo quente do engine.

### 3. A biblioteca padrão é suficiente para a primeira fundação

O incremento começa sem dependências de runtime externas. A CLI usa `flag`; descoberta e hashing usam `io`, `fs`, `filepath` e `crypto/sha256`; CARs usam `archive/zip`; XML usa `encoding/xml`; logs usam `log/slog`; JSON usa `encoding/json`; concorrência usa `context`, canais e primitivas de `sync`.

O analisador Java inicial será um extrator léxico conservador e tolerante, limitado a pacotes, imports, tipos, métodos, anotações, literais e relações diretas observáveis no corpus. Ele só emitirá uma contribuição quando conseguir fornecer um locador; construções não compreendidas permanecerão incompletas ou não suportadas. Um parser Java mais profundo será selecionado em mudança própria por qualidade semântica, licença, segurança, consumo e custo de interoperabilidade, e não por preferência.

Bibliotecas como Cobra/Viper, tree-sitter, frameworks web, ORM, GraphQL, gRPC, caches, programação reativa e containers de DI não entram agora. Uma dependência futura precisa reduzir complexidade total ou fornecer semântica que a biblioteca padrão não oferece, e deve ser aprovada antes de `go get`.

Alternativas consideradas:

- **Cobra e Viper desde o primeiro comando:** oferecem conveniência para árvores grandes e múltiplas fontes de configuração, mas o subconjunto inicial cabe em flags e structs explícitos;
- **tree-sitter imediatamente:** amplia cobertura sintática, porém introduz binding, gramáticas e possíveis restrições de CGO/empacotamento antes de medir a suficiência do recorte mínimo;
- **JavaParser em processo Java:** melhora semântica Java, mas adiciona um segundo runtime ao Agent antes de validar o protocolo externo e o benefício no corpus.

### 4. A CLI é o primeiro modo de execução do `Manu Agent`

Neste incremento, “Agent” não significa um daemon autônomo nem IA local. É o processo confiável que recebe uma configuração explícita, lê uma montagem local autorizada, executa o pipeline determinístico e grava resultados em um destino separado. A CLI compõe esse processo e oferece inicialmente intenções equivalentes a:

```text
manu analyze     analisar uma raiz e produzir o resultado comum
manu inspect     resumir um resultado já produzido
manu benchmark   executar e comparar os cenários definidos
manu version     identificar binário, contrato e toolchain
```

Cada comando aceita saída humana ou JSON versionado. O contrato estruturado será marcado como `v1alpha1`; sua evolução antes de estabilidade pode exigir migração explícita, nunca uma mudança silenciosa. Códigos de saída distinguem sucesso, resultado parcial, uso/configuração inválida e falha técnica.

O Agent inicial não contém IA nem o banco principal. Em uma instalação futura, ele poderá enviar resultados autorizados para a célula SaaS ou self-hosted; esse transporte não pertence a esta mudança.

Alternativas consideradas:

- **API HTTP primeiro:** adiciona servidor, ciclo de vida e superfície de segurança sem melhorar o benchmark local;
- **daemon de filesystem:** exige estado, eventos e recuperação operacional antes de validar uma análise explícita;
- **LLM dentro do Agent:** aumenta consumo, hardware e política de dados e mistura extração determinística com geração.

### 5. O pipeline é streaming, limitado e determinístico

O pipeline executará descoberta, hashing e análise com um pool configurável e pequeno. Leitores e analisadores recebem `context.Context`; não iniciam goroutines sem dono; fecham recursos no mesmo escopo que os abre. Conteúdo é processado em fluxo e em lotes limitados, evitando manter o corpus ou um grafo completo em memória.

Contribuições são enviadas a um coletor comum e ordenadas por chaves estáveis antes da serialização. Identidades derivam da identidade da fonte, caminho relativo normalizado, hash do artefato, analisador e versão do método. Instante, `run_id` e métricas são metadados de execução e não participam da equivalência factual.

O resultado será dividido em um manifesto de execução e sequências JSON por artefato/contribuição, gravadas de forma atômica no diretório de saída. Isso permite streaming e comparação sem depender de banco. Texto integral não será emitido por padrão; evidências usam locadores e trechos mínimos sanitizados quando indispensáveis.

Alternativas consideradas:

- **um único JSON em memória:** simplifica uma demonstração pequena, mas distorce o benchmark de ERPNext e aumenta o pico de memória;
- **persistir diretamente em PostgreSQL:** exercita a fronteira futura, porém mistura a seleção do runtime com uma decisão física ainda não validada;
- **ordem de conclusão concorrente:** maximiza throughput bruto, mas torna diffs e avaliações instáveis.

### 6. O estado incremental é reconstruível e fica fora da fonte

O diretório de saída manterá um estado reconstruível indexado por identidade/hash do artefato, versão do contrato e identificador/versão do analisador. Uma repetição reutiliza somente contribuições cujas chaves continuarem equivalentes. Alterações invalidam o artefato e, quando houver relações diretas conhecidas, seus dependentes imediatos; limitações de invalidação são registradas.

O benchmark tem três cenários:

1. primeira análise sem estado anterior;
2. repetição sem mudança com o estado anterior;
3. atualização localizada fornecida por uma segunda revisão ou por um overlay efêmero, sem escrever na fonte original.

O overlay é identificado como simulação e não pode ser confundido com medição integral de filesystem. Resultados factuais são comparados ignorando somente metadados próprios da execução.

Alternativas consideradas:

- **cache opaco em memória:** não permite repetir processos nem auditar por que algo foi reutilizado;
- **watcher de filesystem:** mede eventos, não a correção da invalidação, e introduz um daemon fora do escopo;
- **modificar o corpus externo para simular atualização:** viola a autorização somente leitura e prejudica a reprodutibilidade.

### 7. Segurança de leitura é parte do desenho do parser

Links simbólicos não serão seguidos por padrão. Caminhos são normalizados e validados contra a raiz. Arquivos especiais, dispositivos e sockets são recusados. A configuração limita quantidade de arquivos, bytes por arquivo e total, tempo, concorrência, quantidade de membros, tamanho descompactado e razão de expansão de arquivos ZIP. Membros com caminhos absolutos, `..`, criptografia não suportada ou tipo inesperado são rejeitados.

O runtime nunca chama executáveis da fonte. Nomes conhecidos de segredos e chaves privadas ficam excluídos por padrão, e a configuração pode ampliar exclusões. Logs estruturados carregam identificadores e métricas, não conteúdo bruto, token ou credencial. Erros adicionam operação e locador sem repetir conteúdo sensível.

Alternativas consideradas:

- **confiar no corpus local:** os mesmos analisadores serão executados em bases de clientes; segurança adicionada depois deixaria contratos e testes incompatíveis;
- **extrair CARs para disco:** facilita ferramentas tradicionais, mas amplia risco de traversal, resíduos e volume temporário;
- **seguir links automaticamente:** encontra mais arquivos, porém pode atravessar autorização e ciclos.

### 8. O benchmark mede o processo real e documenta a precisão da métrica

O relatório registra hardware e sistema disponíveis, versão do binário/toolchain, configuração, fonte/revisão, duração total e por etapa, bytes lidos/escritos, contagens, concorrência efetiva, reutilização e falhas. Em Linux, memória inclui o pico residente reportado pelo processo/sistema quando disponível e amostras do heap Go; em outros ambientes, a ausência da métrica é declarada, não estimada como se fosse equivalente.

Testes de unidade e integração usam fixtures. A execução sobre os três caminhos externos é opt-in, somente leitura e fixa as revisões/hashes do manifesto. Microbenchmarks usam `testing.B` e comparação estatística somente onde houver repetição suficiente; um único número local não decide capacidade comercial.

Alternativas consideradas:

- **medir apenas tempo total:** esconde alocação, etapas dominantes e trabalho refeito;
- **benchmark sintético isolado:** é útil para hot paths, mas não representa diversidade e escala do corpus;
- **profiling permanente em produção:** não existe produção neste estágio e o custo não é necessário para a linha de base local.

### 9. A distribuição inicial será binário estático e imagem mínima

O build Linux usará `CGO_ENABLED=0` enquanto nenhuma dependência justificar CGO. Uma imagem multi-stage compila com toolchain fixado e executa o binário em `scratch` como UID/GID não privilegiado, sem shell. A fonte é montada em um caminho somente leitura e a saída em volume separado gravável. As arquiteturas iniciais de build são `linux/amd64` e `linux/arm64`.

Essa imagem contém somente a CLI/Agent do microcorte. Docker Compose e a célula completa virão quando existirem os demais processos e infraestrutura; criar agora um Compose de um único binário sem dependências não validaria a implantação do produto.

Alternativas consideradas:

- **imagem de distribuição Linux completa:** facilita depuração interativa, mas aumenta superfície e tamanho sem necessidade de runtime;
- **CGO desde o início:** pode habilitar bibliotecas nativas, porém reduz portabilidade e torna build multi-arquitetura mais caro;
- **Compose imediato:** sugeriria que banco, IA e plataforma já possuem contratos operacionais definidos.

### 10. Verificação começa no contrato e nos limites de segurança

A suíte incluirá testes de tabela para identidade, hashing, seleção e cobertura; golden tests para saída estruturada; testes de integração do pipeline; fuzzing para normalização de caminhos, ZIP e XML; testes de cancelamento e concorrência; e benchmarks dos hot paths observados. Fixtures não conterão segredos nem dependerão dos três repositórios externos.

As verificações mínimas da aplicação serão formatação, `go vet`, testes, testes com detector de corrida quando suportado, build das arquiteturas e varredura de vulnerabilidades do grafo efetivamente usado. Ferramentas adicionais só entram quando disponíveis ou aprovadas; a ausência de uma ferramenta opcional fica registrada.

## Risks / Trade-offs

- **O extrator Java mínimo parecer um analisador semântico completo** → nomear cobertura e método, exigir locadores e marcar decisões/fluxos não reconstruídos como incompletos ou não suportados.
- **Biblioteca padrão gerar código próprio demais para parsing** → limitar o parser ao microcorte e usar resultados/benchmarks para decidir tree-sitter, JavaParser ou outra especialização em mudança separada.
- **JSON em arquivos tornar consultas futuras lentas** → tratá-lo como formato operacional reconstruível do experimento, não como persistência definitiva.
- **Ordenação determinística reduzir throughput** → fazer merge limitado por lote e medir o custo; rastreabilidade e regressão reproduzível prevalecem no primeiro corte.
- **Cache reutilizar resultado incompatível** → incluir hash, contrato, analisador e versão do método na chave e preferir reprocessar diante de ambiguidade.
- **Regras padrão de segredo omitirem arquivos úteis ou deixarem escapar conteúdo** → saída sem texto integral por padrão, exclusões configuráveis, sanitização e fixtures negativas; política explícita continua sendo a autoridade.
- **Imagem `scratch` dificultar diagnóstico** → oferecer o mesmo binário fora do contêiner e manter logs/saída estruturados; uma imagem de debug pode ser construída localmente sem virar artefato de produção.
- **Go não oferecer a melhor biblioteca para toda linguagem** → manter o contrato e a fronteira de analisadores independentes do runtime e exigir evidência antes de acrescentar um processo especializado.

## Migration Plan

Não há aplicação ou dados existentes a migrar. A aplicação seguirá esta ordem:

1. confirmar o caminho canônico `github.com/pedrogpaulino/manu` e disponibilizar o toolchain local Go 1.26.6;
2. registrar o ADR Go-first e alinhar a arquitetura canônica;
3. criar módulo, contrato e CLI mínimos com testes;
4. acrescentar leitura segura, analisadores e estado incremental em incrementos verificáveis;
5. adicionar benchmark, imagem e documentação somente depois que o pipeline local estiver correto;
6. executar fixtures por padrão e o corpus externo apenas por comando explícito e somente leitura.

Se o microcorte invalidar Go como runtime principal, os resultados e métricas serão preservados. O ADR será substituído, não apagado, e o código experimental poderá ser removido em uma mudança específica; não há dados de produção a converter.

## Open Questions

As questões sobre o caminho do módulo e a fonte do toolchain foram resolvidas
nesta aplicação. A escolha de bibliotecas para analisadores especializados e a
necessidade de um protocolo externo permanecem condicionadas às medições e às
mudanças OpenSpec próprias, sem alterar a fundação Go-first por suposição.
