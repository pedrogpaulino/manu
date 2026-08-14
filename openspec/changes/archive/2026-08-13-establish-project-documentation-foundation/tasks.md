## 1. Estabelecer produto e linguagem

- [x] 1.1 Criar `PRODUCT.md` definindo o Manu como plataforma que transforma fontes técnicas e documentais em uma base de conhecimento viva, com Knowledge Engine como núcleo e catálogo, wiki, grafo, busca, chat, onboarding, impacto e investigação como experiências derivadas.
- [x] 1.2 Registrar em `PRODUCT.md` sustentação, arquitetura e usuários de negócio como públicos iniciais, o comprador empresarial, os problemas prioritários e os resultados mensuráveis esperados.
- [x] 1.3 Definir em `PRODUCT.md` um MVP vertical com duas a quatro aplicações reais, ingestão de documentos existentes, relações com evidências, páginas geradas/editáveis, revisão por especialista e demonstração de uso do conhecimento; conectar cada item a uma hipótese e sinal de validação.
- [x] 1.4 Registrar como não objetivos do MVP a integração com ferramentas de chamados, ingestão de logs, métricas e traces, diagnóstico automático de causa raiz, Control Plane e SaaS compartilhado operacional.
- [x] 1.5 Criar `DOMAIN.md` com o glossário e modelo conceitual inicial, incluindo `Organization`, `Source`, `Artifact`, `Observation`, `Entity`, `Relationship`, `Knowledge Claim`, `Evidence`, `Provenance`, `Wiki Page`, `Revision`, `Review` e `Curation`, sem introduzir tabelas, structs ou decisões de persistência.
- [x] 1.6 Distinguir em `DOMAIN.md` conhecimento observado, gerado e curado e esclarecer, ou registrar como questões abertas, as diferenças entre `System`, `Application`, `Service`, `Component`, `Business Process` e `Flow`.
- [x] 1.7 Revisar `PRODUCT.md` e `DOMAIN.md` em conjunto para garantir nomes canônicos, ausência de definições conflitantes e separação entre o núcleo e suas experiências.

## 2. Registrar arquitetura, segurança e implantação

- [x] 2.1 Criar `ARCHITECTURE.md` com contexto, restrições e uma visão C4 simplificada de fontes, Manu Agent, Knowledge Engine, plataforma, AI Gateway, PostgreSQL/pgvector e consumidores da base de conhecimento.
- [x] 2.2 Documentar em `ARCHITECTURE.md` o fluxo conceitual de descoberta, parsing, observações, correlação, claims, evidências, proveniência, System Graph, geração documental, revisão e publicação.
- [x] 2.3 Documentar a preservação de conteúdo curado diante de novas análises, incluindo sinalização de desatualização ou conflito e proposta de revisão em vez de sobrescrita silenciosa.
- [x] 2.4 Documentar `Organization` como fronteira transversal de dados, documentos, busca, jobs, segredos, Agents, políticas, IA e auditoria, sem definir prematuramente o modelo físico de isolamento.
- [x] 2.5 Documentar a arquitetura celular e os modos SaaS compartilhado, SaaS dedicado e self-hosted, registrando uma organização por instalação em Docker Compose/VPS como recorte inicial e Control Plane como opção futura.
- [x] 2.6 Separar política da instalação, política da fonte e permissão do usuário para processamento, transferência e visualização de conteúdo sensível; não representar esses controles como feature flags.
- [x] 2.7 Verificar que a arquitetura permaneça portável e model agnostic, sem dependência obrigatória de cloud, provedor de IA ou serviço fora do MVP.
- [x] 2.8 Criar `docs/decisions/README.md` com política, estados, convenção de nomes e template mínimo para ADRs; criar ADRs somente para escolhas aceitas, difíceis de reverter e que resultem de trade-offs reais.

## 3. Criar entrada e orientação operacional

- [x] 3.1 Criar `README.md` como índice curto, com estado atual, síntese do núcleo do Manu, mapa das fontes de verdade e links relativos válidos, sem duplicar seu conteúdo.
- [x] 3.2 Criar `AGENTS.md` usando o workflow `create-agentsmd`, com instruções operacionais verificáveis, ordem de leitura, convenções atuais, uso do OpenSpec e critérios para manter a documentação coerente.
- [x] 3.3 Incluir integralmente em `AGENTS.md` a seção `Development workflow` definida em `design.md`, atribuindo planejamento, arquitetura, requisitos, OpenSpec, design, decomposição e orquestração ao agente primário e toda implementação ou correção de código a subagentes com papel `implementer`.
- [x] 3.4 Registrar em `AGENTS.md` GPT-5.6 Sol para planejamento/orquestração e GPT-5.6 Luna para implementação, proibindo explicitamente o uso de Sol para implementar ou corrigir código.
- [x] 3.5 Garantir que `AGENTS.md` não anuncie comandos, testes, diretórios ou ferramentas inexistentes e que referencie `PRODUCT.md`, `ARCHITECTURE.md` e `DOMAIN.md` em vez de copiá-los.
- [x] 3.6 Verificar que o workflow instrua o agente primário a revisar mudanças contra o OpenSpec e a redelegar qualquer correção necessária a um `implementer`, sem executá-la diretamente.

## 4. Revisar transversalmente a fundação

- [x] 4.1 Revisar `PRODUCT.md`, `DOMAIN.md` e `ARCHITECTURE.md` juntos para remover duplicação, alinhar termos e garantir que requisitos de produto não apareçam como decisões técnicas ou modelo físico.
- [x] 4.2 Confirmar que todos os documentos apresentam Knowledge Engine e base de conhecimento viva como núcleo, sem reduzir o Manu a grafo, wiki, chat ou investigação isoladamente.
- [x] 4.3 Confirmar que o MVP documentado inclui descoberta sobre aplicações reais, documentos como fonte, relações com evidências, catálogo/documentação mínima, wiki editável e revisão humana, tratando investigação como demonstração importante e não como o núcleo exclusivo.
- [x] 4.4 Confirmar que especialistas podem revisar, corrigir e enriquecer conhecimento para os usuários autorizados da organização e que nova análise não sobrescreve silenciosamente conteúdo curado.
- [x] 4.5 Confirmar que claims conflitantes preservam evidências, proveniência, temporalidade e estado de revisão em vez de produzir falsa certeza.
- [x] 4.6 Confirmar que o produto está documentado como tenancy-ready, mas que multitenancy compartilhado operacional e Control Plane não foram incorporados ao MVP.
- [x] 4.7 Confirmar que integração com tickets e dados operacionais permaneça uma opção futura sem bloquear orientação de investigação baseada no conhecimento disponível.

## 5. Validar os documentos

- [x] 5.1 Percorrer todos os links a partir do `README.md` e corrigir referências quebradas ou circulares que prejudiquem a navegação.
- [x] 5.2 Procurar placeholders, contradições, conteúdo duplicado e termos sem definição; corrigir cada ocorrência ou registrá-la explicitamente como questão aberta.
- [x] 5.3 Verificar que cada afirmação materialmente incerta esteja identificada como hipótese ou opção futura e que decisões aceitas tenham justificativa rastreável.
- [x] 5.4 Verificar que conhecimento observado, gerado e curado esteja claramente separado nos documentos e que o idioma e os termos canônicos sejam usados consistentemente.
- [x] 5.5 Executar `openspec validate establish-project-documentation-foundation` e corrigir todos os erros antes de considerar a mudança concluída.
