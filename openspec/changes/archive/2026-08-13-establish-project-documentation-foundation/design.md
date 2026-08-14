## Context

O repositório contém apenas a configuração inicial do OpenSpec; ainda não há código nem documentação canônica. A exploração esclareceu que o Manu não é primariamente um grafo, wiki, chat ou ferramenta de investigação: seu núcleo é o Knowledge Engine, que transforma fontes técnicas e documentais em uma base de conhecimento viva. Os demais recursos são experiências sobre essa base. A documentação do projeto precisa preservar essa distinção, assim como decisões sobre curadoria, conteúdo sensível e modalidades de implantação, sem converter a visão inicial em desenho físico prematuro.

## Goals / Non-Goals

**Goals:**

- Dar a cada tipo de conhecimento do projeto uma fonte de verdade clara, pequena e navegável.
- Manter o Knowledge Engine e a base de conhecimento viva no centro da narrativa do produto.
- Tornar explícito se uma afirmação é restrição atual, decisão aceita, hipótese ou opção futura.
- Distinguir conhecimento observado, gerado e curado, preservando evidências e autoria humana.
- Registrar um MVP vertical que prove descoberta, correlação, documentação/wiki e revisão humana sobre aplicações reais.
- Manter arquitetura conceitual, modelo de domínio e modalidades de implantação independentes do modelo físico e de um provedor de cloud.
- Permitir que `AGENTS.md` oriente o trabalho sem duplicar produto, arquitetura ou domínio.
- Definir como documentos canônicos, ADRs e mudanças OpenSpec evoluem juntos.

**Non-Goals:**

- Implementar qualquer capacidade do produto nesta mudança documental.
- Escolher todos os parsers, provedores de IA, bibliotecas ou serviços futuros.
- Descrever APIs, tabelas, pacotes Go ou componentes React antes de eles existirem.
- Projetar agora Control Plane, SaaS compartilhado operacional ou isolamento físico definitivo de tenants.
- Incluir no MVP integração com ferramentas de chamados ou ingestão de logs, métricas e traces.
- Implantar portal para a documentação do próprio repositório, gerador de site ou catálogo externo.
- Tratar a visão inicial como compromisso de entregar simultaneamente todas as experiências do produto.

## Decisions

### 1. Markdown versionado será o formato canônico inicial do projeto

Os documentos do repositório serão arquivos Markdown legíveis diretamente. Diagramas pequenos poderão usar Mermaid quando adicionarem clareza, acompanhados por texto que preserve seu significado fora de um renderizador específico.

**Por quê:** não há ainda volume ou público que justifique uma ferramenta de publicação; arquivos simples funcionam offline, são revisáveis e podem alimentar MkDocs, Docusaurus ou outro portal no futuro.

**Alternativas consideradas:** iniciar a documentação do projeto em um portal ou catálogo como Backstage. Essas opções adicionariam operação e taxonomia antes de o conteúdo estabilizar. Esta decisão não limita a wiki que fará parte do produto Manu.

### 2. Cada documento terá uma responsabilidade exclusiva

```text
README.md
  └─ índice e porta de entrada
     ├─ PRODUCT.md          problema, públicos, valor, experiências e MVP
     ├─ ARCHITECTURE.md     restrições, fluxos, componentes e implantação
     ├─ DOMAIN.md           linguagem e modelo conceitual
     ├─ AGENTS.md           regras operacionais do repositório
     ├─ docs/decisions/     decisões aceitas e seu contexto
     └─ openspec/           mudanças propostas, especificações e tarefas
```

- `README.md` apresenta o projeto, seu estado e links; não replica as demais páginas.
- `PRODUCT.md` define o Manu como base de conhecimento viva, descreve públicos, necessidades e experiências, e conecta cada item do MVP a uma hipótese e sinal de validação.
- `ARCHITECTURE.md` descreve fontes, Knowledge Engine, Agent, plataforma, AI Gateway, fluxo até a base de conhecimento, implantação inicial e invariantes de segurança e portabilidade. Decisões aceitas apontam para ADRs.
- `DOMAIN.md` define a linguagem ubíqua e as diferenças entre conhecimento descoberto, inferido e curado. Não define tabelas ou structs.
- `AGENTS.md` contém somente instruções acionáveis: ordem de leitura, convenções, comandos existentes, critérios de verificação, regras de manutenção e a política obrigatória de planejamento/orquestração e implementação delegada. Detalhes conceituais são referenciados, não copiados.
- `docs/decisions/README.md` explica o processo e contém um template mínimo de ADR. ADRs usam nomes como `0001-<titulo-kebab-case>.md` e estados `Proposed`, `Accepted`, `Superseded` ou `Rejected`.
- `openspec/` registra mudanças delimitadas. Ele não substitui a visão canônica; uma mudança aplicada deve atualizar os documentos afetados.

**Alternativa considerada:** um único documento de visão. Ele mistura níveis de decisão, cresce rapidamente e dificulta descobrir qual conteúdo deve ser atualizado.

### 3. O núcleo e suas experiências serão documentados separadamente

Os documentos representarão a visão nesta ordem:

```text
Código + arquivos + APIs + bancos + configurações + documentos
                              │
                              ▼
                      Knowledge Engine
                              │
                 descoberta • correlação • evidência
                              │
                              ▼
                   Base de conhecimento viva
                              │
       ┌──────────┬───────────┼──────────┬─────────────┐
       ▼          ▼           ▼          ▼             ▼
    Catálogo     Wiki       Grafo      Busca/chat   Ações assistidas
                                                       │
                                               onboarding • impacto
                                                   • investigação
```

Nenhuma experiência isolada será descrita como o produto inteiro. Investigação é uma aplicação importante do conhecimento, mas não substitui documentação, onboarding, exploração ou análise de impacto.

O MVP documentado deverá provar um fluxo vertical com duas a quatro aplicações reais, documentos existentes, relações sustentadas por evidências, páginas geradas/editáveis, revisão por especialista e uma demonstração de uso do conhecimento. Integrações externas com sistemas de tickets ficam para uma evolução.

### 4. O estado epistemológico e o ciclo do conhecimento serão visíveis

Afirmações de produto e arquitetura que possam ser confundidas com compromissos serão classificadas como:

- **Current constraint:** condição que limita o trabalho agora.
- **Accepted decision:** escolha vigente, com ADR quando a justificativa precisar ser preservada.
- **Hypothesis:** suposição que precisa de validação.
- **Future option:** direção possível, sem compromisso de entrega.

O conhecimento planejado para o produto será distinguido como:

- **Observed knowledge:** encontrado diretamente por um analisador em uma fonte.
- **Generated knowledge:** síntese ou explicação produzida a partir de observações e evidências.
- **Curated knowledge:** contribuição criada, corrigida ou aprovada por especialista.

A wiki planejada para o MVP deverá preservar o conteúdo curado quando uma nova análise ocorrer. Em vez de sobrescrever silenciosamente, o sistema deverá poder sinalizar desatualização ou conflito e propor revisão.

```text
Descoberta → Geração → Revisão → Publicação
     ▲                                │
     └──── nova análise e conflito ───┘
```

Listas genéricas de futuro não formarão um roadmap implícito.

### 5. O modelo conceitual cobrirá conhecimento e colaboração

`DOMAIN.md` deverá definir inicialmente, sem comprometer persistência:

- `Organization`: fronteira de conhecimento, políticas e autorização de uma empresa cliente.
- `Source`: origem configurada a ser analisada, incluindo repositórios, filesystem, APIs, bancos e documentos.
- `Artifact`: unidade concreta descoberta em uma fonte.
- `Observation`: resultado produzido por um analisador sobre um artefato.
- `Entity` e `Relationship`: elementos canônicos do System Graph.
- `Knowledge Claim`: afirmação sobre o ambiente sustentada ou contestada por evidências.
- `Evidence` e `Provenance`: suporte verificável e histórico de origem, tempo e método.
- `Wiki Page` e `Revision`: conteúdo publicável e sua evolução.
- `Review` e `Curation`: avaliação e melhoria por especialista.

As diferenças entre `System`, `Application`, `Service`, `Component`, `Business Process` e `Flow` permanecerão explícitas; ambiguidades não resolvidas serão registradas como questões abertas, não disfarçadas como definições.

### 6. A arquitetura será tenancy-ready e suportará células

`ARCHITECTURE.md` descreverá `Organization` como fronteira obrigatória, mesmo quando uma instalação tiver somente uma organização. O destino arquitetural permitirá que a mesma aplicação seja empacotada como:

- SaaS compartilhado, com múltiplas organizações isoladas em uma célula;
- SaaS dedicado, operado pelo Manu para uma organização;
- self-hosted, operado no ambiente do cliente.

O MVP usará uma organização por instalação, seja em VPS operada pelo Manu ou no cliente. Dados, documentos, busca vetorial, jobs, segredos, Agents, políticas e auditoria deverão ser conceitualmente vinculados à organização para evitar uma migração estrutural futura.

Um futuro Control Plane poderá provisionar, licenciar e atualizar células sem precisar acessar o conhecimento do cliente. Sua implementação, assim como o modo compartilhado, não faz parte do MVP.

**Alternativas consideradas:** implementar multitenancy compartilhado completo desde o início, aumentando risco e escopo; ou omitir a fronteira organizacional e assumir single-tenant permanentemente, tornando uma evolução posterior invasiva.

### 7. Política de conteúdo será separada de autorização

O desenho documentará três controles independentes:

- política da instalação sobre conteúdo que pode sair do ambiente;
- política da fonte sobre processamento de metadados, trechos ou conteúdo completo;
- permissão do usuário para visualizar evidências e conteúdo disponível.

Esses controles não serão chamados de feature flags. São políticas de tratamento de conteúdo e autorização. Configurações iniciais poderão ser simples, mas a distinção deverá permanecer desde a fundação arquitetural.

### 8. A documentação inicial será escrita em português brasileiro

Termos de domínio terão um nome canônico, preferencialmente em inglês quando também forem usados em código ou integrações, com definição em português. Isso preserva clareza para o público inicial sem criar versões concorrentes.

**Alternativa considerada:** documentação bilíngue desde o início. Sem necessidade concreta, duas versões aumentariam custo e divergência.

### 9. A fundação documental terá verificações leves

Na primeira versão, a verificação será manual e orientada por checklist: links válidos, ausência de placeholders, responsabilidade única, termos coerentes, distinção entre decisão e hipótese e alinhamento entre núcleo, experiências e MVP. Automação será adicionada quando existir uma ferramenta de projeto ou CI estável.

### 10. O agente primário planejará e orquestrará, mas não implementará código

O `AGENTS.md` deverá incluir integralmente a seguinte política operacional:

```markdown
## Development workflow

The primary agent is the planner and orchestrator.

### Planning

Planning, architecture decisions, requirements analysis,
OpenSpec proposals, design and task decomposition are handled
by the primary agent.

The primary agent must not implement application code.

### Implementation

Once an OpenSpec change is sufficiently specified:

1. Break the implementation into small independent tasks.
2. Delegate implementation to `implementer` subagents.
3. Wait for the implementers to finish.
4. Review their changes against the OpenSpec.
5. If corrections are required, delegate them back to an
   `implementer`.
6. Never implement or fix the code directly from the primary agent.

### Model policy

- Planning/orchestration: GPT-5.6 Sol
- Implementation: GPT-5.6 Luna
- Sol must never be used for implementation.
```

Essa política define papéis, não capacidades do produto. O agente primário é responsável por planejamento, decisões, decomposição, delegação e revisão; toda implementação ou correção de código deve ser executada por subagentes com papel `implementer`. A política deverá ser preservada mesmo quando outras orientações forem acrescentadas ao `AGENTS.md`.

## Risks / Trade-offs

- **[Documentos divergirem entre si]** → Definir responsabilidade exclusiva, usar links em vez de cópia e atualizar documentos afetados na mesma mudança.
- **[Uma experiência ser confundida com o produto inteiro]** → Apresentar sempre Knowledge Engine e base viva antes das experiências derivadas.
- **[A visão parecer arquitetura definitiva]** → Rotular hipóteses e opções futuras; reservar ADRs para escolhas aceitas.
- **[Conteúdo humano ser perdido em uma reanálise]** → Registrar preservação, versionamento, propostas e revisão como invariantes da wiki planejada.
- **[Fontes conflitantes produzirem falsa certeza]** → Manter claims, evidências, proveniência, temporalidade e estado de revisão separados.
- **[Vazamento entre organizações]** → Tratar organização como fronteira transversal de dados, documentos, jobs, busca, IA e autorização, mesmo no MVP dedicado.
- **[Políticas de código serem reduzidas a uma configuração binária]** → Separar processamento, transferência e visualização.
- **[Excesso de documentação antes do aprendizado real]** → Limitar o conjunto inicial e conectar conteúdo a decisão, onboarding ou validação.
- **[AGENTS.md ficar grande e obsoleto]** → Manter instruções verificáveis e apontar para fontes canônicas.
- **[O agente primário implementar durante o apply]** → Tornar explícita a separação de papéis e exigir nova delegação a um `implementer` para qualquer correção encontrada na revisão.
- **[Vocabulário antecipar o banco de dados]** → Separar modelo de domínio de modelo físico e adiar mapeamentos para designs específicos.

## Migration Plan

1. Criar `PRODUCT.md` e `DOMAIN.md` a partir da visão consolidada, registrando lacunas como questões abertas em vez de inventar respostas.
2. Criar `ARCHITECTURE.md` alinhado ao núcleo, ciclo do conhecimento, políticas e modalidades de implantação.
3. Revisar os três documentos transversalmente para remover duplicação e alinhar termos.
4. Criar `README.md`, `AGENTS.md` e política de ADRs apontando para as fontes canônicas.
5. Validar navegação, limites do MVP, estados epistemológicos e coerência em um checkout limpo.
6. Usar futuras mudanças OpenSpec para transformar cada capacidade do produto em requisitos e implementação rastreáveis.

Como a mudança apenas adiciona arquivos, rollback consiste em revertê-la; não há migração de dados ou compatibilidade operacional.
