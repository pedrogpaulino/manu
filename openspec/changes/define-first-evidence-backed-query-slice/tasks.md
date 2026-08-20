## 1. Fixar decisões, dependências e fronteiras

- [x] 1.1 Verificar nas fontes oficiais as versões estáveis e suportadas de PostgreSQL, pgvector e dependências Go candidatas, documentando suporte, licença, segurança, tamanho e motivo de cada dependência antes de alterar `go.mod` ou imagens.
- [x] 1.2 Registrar ADRs aceitos para PostgreSQL/pgvector como persistência e projeção inicial e para o `AI Gateway` independente de provedor, alinhando o índice de decisões e `ARCHITECTURE.md` sem promover autenticação ou produção a capacidade existente.
- [x] 1.3 Adicionar somente as dependências aprovadas e criar o layout interno do monólito para bundle, evidência, persistência, ingestão, API, recuperação, AI Gateway e avaliação, preservando composição explícita e o Agent sem banco/IA.
- [x] 1.4 Definir configuração tipada e validada para servidor, organização local, banco, limites, política de conteúdo, recuperação, embedding, geração e orçamento, com precedência documentada e sem valores secretos em defaults ou fixtures.

## 2. Estender o contrato do Agent com bundle e evidências

- [x] 2.1 Implementar tipos versionados para manifesto do `Analysis Bundle`, `Evidence Unit`, decisões de persistência/transferência e referências às contribuições, com validação de identidade, integridade, limites e testes de tabela.
- [x] 2.2 Implementar codec em fluxo e escrita atômica de `evidence.ndjson` e do manifesto estendido, preservando leitura de resultados `v1alpha1` sem evidências como entrada limitada e criando golden tests reproduzíveis.
- [x] 2.3 Materializar unidades semanticamente delimitadas nos analisadores genérico, Java e WSO2, mantendo locadores, hashes, truncamento e proveniência e sem emitir arquivo inteiro ou conteúdo sensível por padrão.
- [x] 2.4 Implementar política `allow`/`redact`/`deny` independente para persistência e transferência externa, com sanitização antes da serialização e testes negativos para segredo, prompt injection, conteúdo binário e evidência proibida.
- [x] 2.5 Implementar leitura e envio multipart do bundle em fluxo, com digest factual, limites, cancelamento e testes que comprovem ausência de traversal, carga integral em memória e modificação da fonte.

## 3. Criar schema e persistência canônica

- [x] 3.1 Implementar migrations SQL aditivas para organização, fonte, snapshot, artefato, observação, entidade, relação, evidência, cobertura, lacuna, falha e identidade ativa/histórica, com constraints de escopo e integridade referencial.
- [x] 3.2 Implementar migrations para jobs de ingestão, perfis/itens de embedding, consultas, candidatos, pacotes, afirmações, citações e chamadas de provedor, sem armazenar credenciais ou tornar vetores fonte de verdade.
- [x] 3.3 Implementar `manu migrate` com lock, versão de schema, transações, diagnóstico de incompatibilidade e testes de primeira aplicação, repetição idempotente e falha segura, sem downgrade destrutivo.
- [x] 3.4 Implementar repositórios SQL parametrizados e transações explícitas para a representação canônica, incluindo isolamento por `Organization`, snapshots imutáveis e ativação atômica da visão corrente.
- [x] 3.5 Implementar persistência em lote e idempotência pelo digest factual do bundle, com testes de reenvio, referências órfãs, rollback de bundle inválido e preservação de snapshots anteriores.

## 4. Implementar ingestão assíncrona e projeções iniciais

- [x] 4.1 Implementar estados, etapas, locação e recuperação durável de jobs no PostgreSQL, com executor concorrente limitado no mesmo processo e testes de cancelamento, reinício e ausência de processamento duplicado.
- [x] 4.2 Implementar pipeline de ingestão que valida integralmente o bundle, persiste conhecimento canônico, cria projeções não vetoriais e ativa o snapshot somente quando os invariantes aplicáveis forem satisfeitos.
- [x] 4.3 Implementar retomada e estado `partial` quando embeddings falharem ou forem proibidos, preservando conhecimento textual/relacional e permitindo reconstrução posterior sem reingerir fatos.
- [x] 4.4 Implementar atualização localizada entre snapshots, reutilizando identidades compatíveis, invalidando projeções derivadas afetadas e mantendo histórico e itens não afetados verificáveis.
- [x] 4.5 Criar testes integrados de primeira ingestão, repetição, atualização localizada, falha parcial e concorrência sobre PostgreSQL/pgvector real de teste.

## 5. Oferecer API HTTP local e clientes CLI

- [x] 5.1 Implementar `manu serve` com `net/http`, configuração, timeouts, limites, cancelamento, encerramento gracioso, request ID e validação bloqueante de loopback enquanto não houver autenticação.
- [x] 5.2 Implementar `POST /api/v1/ingestions` e `GET /api/v1/ingestions/{id}` com multipart em fluxo, `202 Accepted`, estados estruturados, idempotência e erros `application/problem+json`.
- [x] 5.3 Implementar contratos base de `POST /api/v1/queries`, `GET /api/v1/queries/{id}` e `GET /api/v1/evidence/{id}`, mantendo consulta síncrona, persistência da execução e escopo fixo de organização.
- [x] 5.4 Implementar `/healthz` e `/readyz`, distinguindo processo vivo de banco/schema pronto sem depender de chamada remota ao provedor.
- [x] 5.5 Escrever e testar o documento OpenAPI do contrato realmente implementado, incluindo exemplos seguros, limites, estados parciais e códigos de erro.
- [x] 5.6 Implementar clientes CLI para enviar bundle, consultar ingestão, perguntar e inspecionar evidência, com saída humana/JSON e códigos de saída coerentes com a CLI existente.

## 6. Implementar projeções e recuperação híbrida

- [x] 6.1 Implementar projeção textual para termos técnicos e campos exatos, com índice reconstruível, filtros por organização/fonte/snapshot e testes de símbolos, configurações, exceções e texto genérico.
- [x] 6.2 Implementar projeção relacional dirigida e expansão limitada a um salto, com fan-out configurável, proveniência de cada aresta e testes contra travessia indevida de escopo.
- [x] 6.3 Implementar perfil imutável de embedding, cache por perfil e hash da evidência, dimensão validada e rebuild completo, recusando mistura de perfis na mesma consulta.
- [x] 6.4 Implementar projeção pgvector e busca cosseno exata para o corpus inicial, medindo latência e recall antes de introduzir qualquer índice aproximado.
- [x] 6.5 Implementar fusão determinística dos rankings exato, textual e vetorial e expansão relacional com configuração registrada, testes de estabilidade e degradação textual/relacional quando embeddings faltarem.
- [x] 6.6 Implementar o compositor do pacote de evidências com limites de unidades, caracteres/tokens, diversidade, deduplicação, política de transferência, gaps materiais e registro dos candidatos incluídos/excluídos.

## 7. Implementar AI Gateway e provedores

- [x] 7.1 Implementar portas independentes de embedding e geração, perfis/configuração, DTOs internos, resultados de uso/latência/modelo e taxonomia normalizada de erros, com provedores simulados determinísticos.
- [x] 7.2 Implementar orçamento, deadlines, cancelamento, batching, retries transitórios limitados e telemetria de tentativas, garantindo que segredo e conteúdo bruto não apareçam em logs ou diagnósticos.
- [x] 7.3 Implementar o adaptador OpenAI para embeddings e Responses API, validando capacidades e saída estruturada e cobrindo o transporte com servidor HTTP falso antes de qualquer teste real.
- [x] 7.4 Implementar o adaptador OpenAI-compatible com protocolo explícito e base URL validada, cobrindo embeddings e geração e validando o contrato inicialmente contra OpenRouter sem assumir equivalência silenciosa.
- [x] 7.5 Verificar configuração independente de provedores/modelos de embedding e geração, incluindo troca do gerador sem reindexação e troca do embedding com rebuild obrigatório.

## 8. Produzir respostas citadas e abstinência

- [x] 8.1 Implementar schema de resposta com texto, afirmações qualificadas, citações por `Evidence Unit`, lacunas e metadados de geração, sem publicar claims como curados.
- [x] 8.2 Implementar validação determinística de IDs, escopo, citações e suporte declarado contra o pacote, com no máximo uma tentativa orçada de reparo e rejeição segura de texto livre inválido.
- [x] 8.3 Implementar abstinência sem chamada externa quando não houver evidência transferível ou suporte suficiente e preservar distinções entre inventário, `Possible Flow`, `Observed Execution` e intenção de negócio.
- [x] 8.4 Integrar consulta, recuperação, pacote, geração, validação e persistência aos endpoints/CLI, permitindo reinspecionar cada afirmação e evidência pelo identificador.
- [x] 8.5 Criar testes de ponta a ponta simulados para resposta sustentada, citação inexistente, evidência irrelevante na referência, prompt injection, política proibitiva, provider timeout e abstinência esperada.

## 9. Avaliar qualidade, custo e incrementalidade

- [x] 9.1 Materializar casos versionados de inventário, proveniência e abstinência na fixture e no Ticketmaster, com claims aceitáveis, evidências esperadas, lacunas, autoria/revisão e atribuição de falha por etapa.
- [x] 9.2 Implementar `manu eval` em modo simulado padrão, medindo extração, ingestão, `evidence_recall_at_k`, precisão, primeira evidência, citações, abstinência, latência, volume e trabalho reutilizado.
- [x] 9.3 Implementar `live eval` opt-in com confirmação explícita de política e orçamento por requisições/tokens/custo, registro do modelo efetivo e interrupção antes de exceder qualquer limite.
- [x] 9.4 Executar a avaliação local sobre Ticketmaster e os testes aplicáveis de heterogeneidade/escala em WSO2 e ERPNext, mantendo fontes somente leitura e separando falha de extração, recuperação, geração e política.
- [x] 9.5 Registrar linha de base com ambiente, configuração, perfis, métricas, conteúdo transferido por identificador/hash, limitações e custos, sem segredo, conteúdo integral, SLA ou comparação inválida entre modelos.

## 10. Empacotar a célula local e documentar operação

- [x] 10.1 Criar Docker Compose mínimo com PostgreSQL/pgvector, migração one-shot e API usando a imagem Manu, versões fixadas, healthchecks, volume persistente e portas publicadas somente em loopback.
- [x] 10.2 Criar configuração de exemplo sem segredo, ignorar arquivos locais sensíveis e documentar chaves externas para OpenAI/OpenRouter, perfis independentes, política de transferência e execução totalmente simulada.
- [x] 10.3 Verificar build e execução Linux amd64/arm64 do Agent e da API, migração, reinício, persistência, readiness e consulta sem provedor real, medindo consumo dos dois componentes.
- [x] 10.4 Atualizar `README.md` e documentos canônicos afetados com fluxo Agent → bundle → API, comandos reais, contratos, limites sem autenticação, reconstrução de projeções e distinção entre conhecimento observado e gerado.

## 11. Revisar segurança, coerência e conclusão

- [x] 11.1 Revisar ameaças de bundle hostil, SQL injection, exposição sem autenticação, SSRF por base URL, prompt injection, vazamento em log, mistura de organização/perfil e abuso de orçamento; adicionar testes para cada controle aplicável.
- [x] 11.2 Executar formatação, `go vet`, suíte completa, detector de corrida quando suportado, fuzzing aplicável, builds estáticos, verificação de módulos/vulnerabilidades e testes de integração/contrato, registrando ferramentas opcionais indisponíveis.
- [x] 11.3 Verificar OpenAPI, migrations, links relativos, ausência de placeholders/segredos, responsabilidade documental e coerência entre proposta, specs, design, ADRs, arquitetura, corpus e protocolo de avaliação.
- [x] 11.4 Executar `openspec validate define-first-evidence-backed-query-slice --strict`, corrigir todas as violações e confirmar que nenhuma capacidade fora do corte é apresentada como implementada antes de concluir a mudança.
