## 1. Alinhar as fontes canônicas

- [x] 1.1 Atualizar `PRODUCT.md` para delimitar o primeiro corte vertical, os papéis das três bases, a profundidade progressiva dos analisadores e os sinais de validação, separando restrições, hipóteses e opções futuras.
- [x] 1.2 Atualizar `ARCHITECTURE.md` com o pipeline do corte, o fallback genérico, as contribuições especializadas, as projeções híbridas, o pacote de evidências, a CLI, o `AI Gateway` com OpenAI como adaptador inicial e a fronteira que adia a escolha de stack.
- [x] 1.3 Revisar `DOMAIN.md` e atualizar somente definições ou relações canônicas indispensáveis ao corte, sem promover estruturas de teste, comandos ou formatos físicos a conceitos do domínio.

## 2. Registrar o corpus de referência

- [x] 2.1 Criar um documento navegável do primeiro corte que registre o formato do manifesto de corpus, autorizações, revisões ou hashes, inclusões, exclusões, papéis de avaliação e critérios de seleção sem copiar as bases externas para o repositório.
- [x] 2.2 Fixar o recorte Ticketmaster para correção Java/Quarkus, excluindo relatórios gerados e material sensível, e registrar a revisão, as áreas semânticas tentadas e as lacunas esperadas.
- [x] 2.3 Selecionar de quatro a seis CARs representativos por diversidade de artefatos WSO2, registrar seus hashes e justificar como a amostra cobre abertura de pacote, inventário e referências declarativas mínimas.
- [x] 2.4 Fixar o inventário completo e o recorte funcional de pedido a faturamento do ERPNext, registrar a revisão e deixar explícito que semântica Python/Frappe profunda não pertence ao primeiro corte.

## 3. Definir perguntas e referências de avaliação

- [x] 3.1 Definir o registro versionado de um caso de avaliação com corpus, revisão, público, pergunta, resposta revisável, autoria, afirmações aceitáveis, evidências esperadas, lacunas e aplicabilidade por analisador.
- [x] 3.2 Preparar o conjunto inicial de perguntas de competência para inventário, relações, fluxos, decisões, configurações, capacidades, erros, evidências e abstinência, sem codificar respostas específicas no comportamento do engine.
- [x] 3.3 Registrar respostas e evidências de referência inicialmente verificáveis para Ticketmaster e para a amostra WSO2, mantendo ERPNext como referência de inventário e escala onde a profundidade semântica não for suportada.

## 4. Definir verificação e benchmark

- [x] 4.1 Documentar as camadas de fixtures determinísticas, contratos locais, corpus de referência e live eval, incluindo como distinguir falha de extração, recuperação e geração.
- [x] 4.2 Definir o modo simulado como padrão e a live eval como execução explícita, com registro de modelo, configuração, tokens, custo, latência e orçamento, sem persistir a secret key ou conteúdo proibido.
- [x] 4.3 Definir as métricas de extração, recuperação, geração, primeira análise, repetição sem mudança e atualização localizada, registrando ambiente e limitações para que os resultados não sejam tratados como SLA.
- [x] 4.4 Documentar o microcorte comparável para a decisão posterior de stack, cobrindo descoberta, hashing, parsing representativo, transformação comum, persistência, concorrência, duração, pico de memória e operação compatível com Compose.

## 5. Preservar navegação e coerência

- [x] 5.1 Atualizar `README.md` com links para os novos documentos do corte e manter cada fonte canônica responsável somente por produto, domínio, arquitetura, operação ou avaliação.
- [x] 5.2 Verificar links relativos, ausência de placeholders e segredos, termos canônicos, estados de decisão e alinhamento entre `Knowledge Engine`, base de conhecimento viva, `AI Gateway` e experiências derivadas.
- [x] 5.3 Executar `openspec validate define-first-knowledge-engine-vertical-slice --strict` e corrigir todas as violações dos artefatos da mudança.
