## Why

Os analisadores atuais produzem uma compreensão limitada de Java e WSO2, enquanto ampliar manualmente essa lógica para cada linguagem, framework e versão criaria implementações duplicadas e rapidamente obsoletas. Ao mesmo tempo, pessoas e agentes de IA repetem a exploração de grandes bases para localizar poucos trechos relevantes, sem aproveitar uma representação comum, versionada e verificável do sistema.

## What Changes

- Ampliar a visão do Manu para que a base de conhecimento viva também forneça contexto verificável e limitado por orçamento a agentes de IA, sem transformar MCP, chat, grafo ou economia de tokens no centro isolado do produto.
- Introduzir um substrato canônico de fatos imutáveis e tipados, com proveniência, cobertura e lacunas, alimentado por frontends especializados substituíveis para linguagens, frameworks, pacotes, configurações e formatos padronizados.
- Separar extração, normalização e derivação para permitir frontends com profundidades diferentes, regras universais reconstruíveis e atualização incremental sem exigir um analisador monolítico nem cobertura uniforme entre linguagens.
- Produzir pacotes de contexto estruturados sob orçamento explícito, combinando sinais exatos, textuais, semânticos e relacionais e preservando locadores que direcionem o consumidor à fonte original.
- Expor uma primeira interface MCP somente leitura para consulta, contexto local, impacto possível e inspeção de evidências, reutilizando a porta de consulta do Knowledge Engine e respeitando a fronteira de `Organization` e as políticas de conteúdo.
- Medir economia de contexto e valor por tarefa contra linhas de base comparáveis, incluindo tokens, chamadas, arquivos lidos, tempo, correção e qualidade das evidências; nenhuma redução percentual será tratada como promessa comercial antes dessa validação.
- Validar o corte em Java/Quarkus, WSO2 e uma terceira família de linguagem representativa do corpus, sem antecipar suporte profundo a vinte linguagens, execução de código não confiável, telemetria de runtime, mutação por MCP ou operação remota de produção.

## Capabilities

### New Capabilities

- `analysis-fact-substrate`: define fatos canônicos, frontends especializados, derivação rastreável, cobertura progressiva, atualização incremental e extensão por linguagem, framework, versão ou formato de intercâmbio.
- `evidence-context-retrieval`: define planejamento e seleção de pacotes mínimos de contexto sob orçamento, com recuperação híbrida, locadores, evidências, proveniência, cobertura, lacunas e continuidade paginada.
- `agent-context-interface`: define a superfície MCP somente leitura para agentes consultarem a base sem acesso direto à fonte de verdade, com escopo autorizado e contratos independentes de modelo.
- `context-efficiency-evaluation`: define benchmarks reproduzíveis para comparar custo, esforço e qualidade de tarefas com e sem o contexto fornecido pelo Manu.

### Modified Capabilities

- `knowledge-engine-comprehension`: amplia a compreensão progressiva para exigir que contribuições especializadas possam sustentar contexto reutilizável por consumidores externos, mantendo fatos específicos, qualificadores e lacunas distinguíveis no contrato comum.

## Impact

- Afeta o modelo interno de análise, os analisadores Java e WSO2, a ingestão e persistência de contribuições, as projeções de busca e grafo, a montagem de pacotes de evidências e a porta de consulta da aplicação.
- Acrescenta uma superfície MCP local somente leitura e contratos estruturados de contexto, sem conceder ao modelo acesso direto ao PostgreSQL ou à `Source`.
- Exige corpus e fixtures para Java/Quarkus, WSO2 e uma terceira família, além de benchmarks comparativos e instrumentação de tokens, chamadas, leituras e qualidade da recuperação.
- Pode incorporar adaptadores opcionais para formatos ou ferramentas como Tree-sitter, SCIP, CPG/SBOM e analisadores de framework, desde que isolados atrás do contrato e sem torná-los dependências obrigatórias do núcleo.
- Atualiza `PRODUCT.md`, `DOMAIN.md`, `ARCHITECTURE.md`, documentação operacional e decisões arquiteturais relacionadas ao substrato factual e à entrega de contexto para agentes.
