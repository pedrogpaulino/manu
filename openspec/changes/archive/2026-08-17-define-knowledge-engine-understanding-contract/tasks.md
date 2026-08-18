## 1. Critério de compreensão no produto

- [x] 1.1 Atualizar `PRODUCT.md` para definir o contrato universal de compreensão e as dimensões que os analisadores podem cobrir progressivamente, sem prometer profundidade uniforme entre fontes.
- [x] 1.2 Registrar em `PRODUCT.md` as famílias iniciais de perguntas de competência e os critérios de correção, cobertura, rastreabilidade, atualidade, incerteza e abstinência que orientarão a validação do MVP.
- [x] 1.3 Refinar o recorte e as hipóteses do MVP em `PRODUCT.md` para usar um corpus heterogêneo de duas a quatro aplicações e referências revisáveis, preservando como abertas a seleção das aplicações e os limiares após a linha de base.

## 2. Linguagem e modelo conceitual

- [x] 2.1 Ampliar o mapa e o glossário de `DOMAIN.md` com os conceitos necessários para cobertura da análise, lacuna explícita e pergunta de competência, mantendo-os livres de modelo físico.
- [x] 2.2 Definir em `DOMAIN.md` `Possible Flow`, `Observed Execution` e sua relação com `Flow` e `Business Process`, deixando explícito que ausência de telemetria impede afirmar ocorrência em runtime.
- [x] 2.3 Definir em `DOMAIN.md` `Capability` e `Knowledge Product`, incluindo a distinção entre um relatório existente no ambiente e um relatório produzido pelo Manu.
- [x] 2.4 Definir em `DOMAIN.md` os contextos `Source Revision`, `Analysis Snapshot`, `Environment`, `Release`, `Build Artifact`, `Deployment`, `Configuration State` e `Documentation Revision`, permitindo vínculos ausentes ou desconhecidos.
- [x] 2.5 Atualizar em `DOMAIN.md` as invariantes e questões abertas afetadas para separar origem, suporte, realidade comportamental, temporalidade e lacunas sem condensá-las em uma pontuação de confiança.

## 3. Fronteiras arquiteturais e decisão aceita

- [x] 3.1 Atualizar `ARCHITECTURE.md` com a relação entre analisadores especializados, o contrato universal, correlação e cobertura parcial, sem escolher protocolo, estrutura de dados ou stack.
- [x] 3.2 Registrar em `ARCHITECTURE.md` que acesso à fonte e transferência para IA são autorizações independentes e que resultados não dependentes de IA continuam disponíveis nos modos SaaS dedicado e self-hosted.
- [x] 3.3 Adicionar a `ARCHITECTURE.md` os contextos necessários para comparações qualificadas entre fonte, análise, configuração, documentação, release, build e implantação.
- [x] 3.4 Criar `docs/decisions/0001-contrato-universal-de-compreensao.md` como ADR `Accepted`, documentando a escolha por analisadores especializados projetados em um contrato universal e os trade-offs contra contratos isolados, grafo único e pipeline centrado em IA.
- [x] 3.5 Atualizar os links de navegação necessários para tornar o ADR e a capacidade OpenSpec localizáveis a partir das fontes canônicas, sem duplicar seus conteúdos.

## 4. Verificação documental

- [x] 4.1 Conferir que `PRODUCT.md`, `DOMAIN.md`, `ARCHITECTURE.md`, o ADR e a especificação usem os mesmos termos para cobertura, fluxos, capacidades, produtos de conhecimento, contextos temporais e papel da IA.
- [x] 4.2 Verificar todos os cenários da especificação contra os documentos atualizados e confirmar que nenhum deles antecipa código, stack, mecanismo de ingestão, telemetria ou promessa de suporte uniforme.
- [x] 4.3 Verificar links relativos, ausência de placeholders, responsabilidade única dos documentos e preservação explícita de decisões, hipóteses e opções futuras.
- [x] 4.4 Executar `openspec validate define-knowledge-engine-understanding-contract --strict` e corrigir somente inconsistências documentais pertencentes a esta mudança.
