## 1. Decisões e contratos canônicos

- [x] 1.1 Registrar em nova ADR a decisão pelo kernel de fatos, frontends substituíveis, derivação com linhagem e formatos de intercâmbio, atualizar o índice de decisões e verificar links e estados conforme `docs/decisions/README.md`.
- [x] 1.2 Atualizar `PRODUCT.md` para incluir contexto para agentes como experiência derivada e redução de exploração/tokens como hipótese mensurável, verificando que MCP, grafo ou chat não substituem o `Knowledge Engine` como centro.
- [x] 1.3 Atualizar `DOMAIN.md` somente com os conceitos públicos necessários ao pacote de contexto e seus consumidores, verificando que tipos físicos de fato, protocolo MCP e algoritmo de ranking permaneçam responsabilidades de arquitetura.
- [x] 1.4 Atualizar `ARCHITECTURE.md` com estágios de contribuição, normalização, derivação e recuperação, perfis de frontend, `Context Package`, fronteira MCP e restrições de autorização, verificando coerência com as ADRs aceitas.
- [x] 1.5 Comparar Tree-sitter, SCIP e Joern no corpus e nas restrições do Agent quanto a cobertura, locadores, determinismo, licença, tamanho, isolamento e build estático, registrar a decisão e verificar que nenhuma dependência opcional seja promovida ao núcleo sem evidência.

## 2. Contrato de fatos e frontends

- [x] 2.1 Implementar tipos e validação de `Canonical Fact`, predicados iniciais, participantes tipados, qualificadores, produtor, evidências e linhagem, verificando unidades válidas e rejeição de identidades, escopos ou valores malformados em testes unitários.
- [x] 2.2 Implementar codificação canônica e identidade determinística de fatos, verificando por testes que ordenação de maps, concorrência e repetição não alterem o digest e que produtores distintos permaneçam distinguíveis.
- [x] 2.3 Implementar manifesto versionado de frontend com famílias, versões, capacidades, limitações, predicados, perfil de execução e schemas de extensão identificados por versão e digest, verificando seleção suportada e cobertura explícita para versões desconhecidas.
- [x] 2.4 Estender o `Analysis Bundle` aditivamente para `v1alpha2`, mantendo `contract_version=v1alpha1` e sequências separadas de manifestos, fatos e extensões, verificando round-trip, leitura de fixtures `v1alpha1` e preservação byte a byte do digest factual anterior.
- [x] 2.5 Implementar validação de contribuições importadas por bundle, incluindo limites, snapshot, locadores, produtor e identidade, versão e digest do schema de extensão, verificando rejeição sem efeitos parciais de índices incompatíveis ou fora do escopo.
- [x] 2.6 Acrescentar golden `v1alpha2` e fixtures `v1alpha1` congeladas, corrupção, extensibilidade e payload excedente, verificando compatibilidade e que erros não ecoem conteúdo negado, segredos ou dados sem limite.

## 3. Persistência factual e ingestão

- [x] 3.1 Criar migration forward-only aditiva para fatos observados e derivados, qualificadores, vínculos de evidência, entradas de derivação, manifestos e versões de regras, verificando aplicação transacional, rollback sem artefatos parciais, constraints de escopo e integridade no catálogo de migrações.
- [x] 3.2 Implementar repositórios transacionais e escopados por `Organization`, `Source` e snapshot para fatos e linhagem, verificando imutabilidade, idempotência, conflito e isolamento em testes PostgreSQL.
- [x] 3.3 Integrar manifestos e fatos à ingestão de bundles sem remover contribuições existentes, verificando atomicidade, repetição idempotente e preservação dos resultados válidos quando uma dimensão falhar.
- [x] 3.4 Implementar leitura canônica e rebuild de `entities`, `relationships` e demais projeções factuais a partir do PostgreSQL, verificando que sejam tratadas como projeções reconstruíveis e possam ser apagadas e refeitas por snapshot sem reanalisar a fonte.
- [x] 3.5 Acrescentar métricas de fatos aceitos, reutilizados, rejeitados, derivados e limitados por fanout, verificando cardinalidade e ausência de conteúdo sensível nos registros operacionais.

## 4. Normalização e corpus heterogêneo

- [x] 4.1 Implementar registro de normalizadores por contribuição e frontend com fallback preservador de extensões, verificando composição aditiva e cobertura quando não houver mapeamento universal seguro.
- [x] 4.2 Migrar o frontend Java/Quarkus para produzir fatos de artefatos, símbolos, definições, referências, chamadas, dependências, configuração e endpoints sustentados, verificando locadores e gaps em fixtures por versão representativa.
- [x] 4.3 Migrar o frontend WSO2 para produzir fatos de elementos nomeados, pertencimento, endpoints, mensagens, dependências e configuração sustentados, verificando correlação entre XML, CAR e evidências internas do pacote.
- [x] 4.4 Implementar o frontend estrutural seguro de Python/Frappe para símbolos, definições, referências ou relações, dependências e configuração aplicáveis, verificando que não execute imports, build ou código da fonte.
- [x] 4.5 Criar manifestos versionados das três famílias com capacidades e versões realmente testadas, verificando que uma família reconhecida não seja anunciada como semanticamente completa.
- [x] 4.6 Implementar detecção e preservação de fatos incompatíveis entre frontends, verificando que conflitos mantenham produtores, evidências e qualificadores separados.
- [x] 4.7 Atualizar o corpus e os goldens de Java/Quarkus, WSO2 e Python/Frappe com predicados e locadores esperados, verificando determinismo do digest factual e cobertura por família.

## 5. Derivação e atualização incremental

- [x] 5.1 Implementar a porta e o registro versionado de regras monotônicas com fila ordenada, deduplicação e ponto fixo, verificando determinismo sob diferentes ordens de entrada.
- [x] 5.2 Implementar regras mínimas para pertencimento e dependência transitiva ou encadeamento de chamadas sustentado, verificando cada resultado contra os fatos de entrada e a versão da regra.
- [x] 5.3 Persistir linhagem e índice reverso de derivação, verificando inspeção completa da cadeia de suporte e rebuild após mudança de versão da regra.
- [x] 5.4 Aplicar limites de iteração, fatos e fanout, verificando que o limite produza cobertura incompleta e lacuna controlada sem publicar relações silenciosamente truncadas.
- [x] 5.5 Implementar diferença de snapshots e invalidação por hash, versão de frontend, regra e schema, verificando reutilização sem mudança e reprocessamento do fanout afetado em alteração localizada.
- [x] 5.6 Comparar atualização incremental com rebuild completo em testes das três famílias, verificando equivalência semântica dos fatos e relações e registrando volume reutilizado e reavaliado.

## 6. Context Package e recuperação sob orçamento

- [x] 6.1 Definir a porta de aplicação e os tipos versionados de intenção, `Context Request`, `Context Package`, item, relação, auditoria, degradação e continuação, verificando validação de escopo, snapshot e limites.
- [x] 6.2 Projetar fatos e relações canônicos para busca exata, textual e relacional e integrá-los ao retriever híbrido existente, verificando identidade de evidência e degradação quando sinais opcionais faltarem.
- [x] 6.3 Implementar a função versionada de utilidade e a seleção gulosa por ganho marginal sob limites de tokens, itens, caracteres e bytes, verificando desempate determinístico, diversidade e razões de exclusão.
- [x] 6.4 Implementar fechamento limitado de suporte para relações e caminhos, verificando que nenhuma relação seja entregue sem as evidências obrigatórias nem ultrapasse o orçamento silenciosamente.
- [x] 6.5 Implementar estimativa de tokens e auditoria separada de contagens reais de provedor, verificando limites com UTF-8, conteúdo vazio, itens grandes e estimadores incompatíveis.
- [x] 6.6 Implementar cursor opaco vinculado a escopo, snapshot, intenção, política, algoritmo e ordenação, verificando continuação sem repetição e rejeição após incompatibilidade ou adulteração.
- [x] 6.7 Aplicar autorização, decisão de transferência, redaction e reinspeção a cada item do pacote, verificando que conteúdo negado não apareça no resultado, cursor, erro ou auditoria.
- [x] 6.8 Adaptar o `Evidence Package` do `AI Gateway` como projeção sanitizada do `Context Package`, verificando compatibilidade da API de consulta e que o `Generator` continue sem acesso à fonte ou ao banco.
- [x] 6.9 Criar testes end-to-end de pergunta, contexto de símbolo, impacto possível e inspeção de evidência nas três famílias, verificando locadores, proveniência, cobertura, gaps e limites.
- [x] 6.10 Implementar e compor a porta produtiva `ContextService` sobre leitura factual canônica, recuperação híbrida, seleção, fechamento de suporte, política, orçamento e continuação, verificando escopo e snapshot, determinismo, pacote válido e operação sem `Generator` em testes unitários e PostgreSQL.

## 7. Interface MCP somente leitura

- [x] 7.1 Adicionar e fixar uma versão estável do SDK Go oficial do MCP compatível com o Go do projeto e o protocolo declarado, verificando licença, `go mod verify`, análise de vulnerabilidades disponível e builds estáticos Linux.
- [x] 7.2 Implementar `manu mcp` por `stdio` atrás da configuração local, com identidade e capacidades versionadas e ordem determinística, verificando inicialização e encerramento limpo com um cliente MCP de teste.
- [x] 7.3 Implementar `manu_query` e `manu_context` sobre a implementação produtiva da porta do `Context Package`, verificando schemas, escopo, orçamento, respostas estruturadas e operação sem `Generator`.
- [ ] 7.4 Implementar `manu_impact` e `manu_evidence`, verificando qualificação de impacto possível, reinspeção de autorização e estados controlados para evidência protegida ou indisponível.
- [ ] 7.5 Implementar recursos `manu://` e continuação nas respostas MCP, verificando revisão histórica estável, indicação não substitutiva de snapshot mais recente e rejeição de cursor incompatível.
- [ ] 7.6 Implementar auditoria e mapeamento seguro de erros MCP, verificando ausência de SQL, Cypher, mutação, credenciais, enumeração entre organizações e detalhes internos em ferramentas e mensagens.
- [ ] 7.7 Adicionar testes de conformidade e integração MCP para negociação, schemas, chamadas, cancelamento, limites e entradas malformadas, verificando que nenhuma chamada contorne a porta da aplicação.
- [ ] 7.8 Documentar comando, configuração de cliente, ferramentas, limites e restrições locais em `README.md`, `docs/cli-http.md` ou documento operacional canônico, verificando todos os exemplos contra o binário construído.

## 8. Avaliação de eficiência de contexto

- [ ] 8.1 Ampliar o schema de casos de avaliação com tarefa, variante, revisão, ferramentas, critérios de sucesso, evidências esperadas e política, verificando fixtures válidas, inválidas e retrocompatíveis.
- [ ] 8.2 Implementar variantes `direct-source`, `text-retrieval` e `manu-context` com configuração equivalente e comparador externo opcional isolado, verificando que cada execução registre diferenças inevitáveis sem misturar resultados.
- [ ] 8.3 Instrumentar tokens medidos e estimados separadamente, chamadas, arquivos, bytes, duração e custo quando observáveis, verificando que métricas indisponíveis permaneçam ausentes em vez de zero.
- [ ] 8.4 Implementar correção, conclusão, recall, precisão, validade de citações, gaps e abstinência por tarefa, verificando fórmulas com casos goldens positivos, negativos e sem evidência.
- [ ] 8.5 Implementar custo e esforço por tarefa correta e sustentada e economia entre variantes comparáveis, verificando resultado indefinido quando não houver sucesso correto.
- [ ] 8.6 Criar casos versionados de localização, explicação e impacto para Java/Quarkus, WSO2 e Python/Frappe, verificando referências e evidências esperadas por especialista ou fixture revisável.
- [ ] 8.7 Gerar relatórios brutos e resumos com digests, amostra, dispersão, configurações e limitações, verificando reprodução e comparação após alteração de frontend, regra ou recuperação.
- [ ] 8.8 Executar a linha de base e a variante Manu no ambiente documentado, registrar os resultados em `docs/evaluation/` e verificar que qualquer economia seja descrita como observada no recorte, não como SLA ou garantia geral.

## 9. Verificação integrada e encerramento

- [ ] 9.1 Executar `gofmt -d` nos arquivos Go alterados, `go vet ./...`, `go test ./... -count=1`, `go mod verify` e `git diff --check`, corrigindo toda falha introduzida pela mudança.
- [ ] 9.2 Executar `go test -race ./...` quando CGO e compilador C estiverem disponíveis e registrar explicitamente a ausência quando não estiverem; executar também a ferramenta opcional de vulnerabilidades quando instalada.
- [ ] 9.3 Produzir builds estáticos `linux/amd64` e `linux/arm64` de `./cmd/manu` com `CGO_ENABLED=0`, verificando que o suporte MCP e os frontends padrão não introduzam dependência dinâmica.
- [ ] 9.4 Executar `go test ./docs`, validação estrutural `docker compose config --quiet` quando a célula for afetada e um fluxo Agent -> bundle -> ingestão -> contexto -> MCP, registrando a revisão e os limites observados.
- [ ] 9.5 Revisar links relativos, placeholders, termos canônicos e responsabilidade única de documentos e executar `openspec validate establish-evidence-backed-context-engine --strict` até a mudança permanecer válida.
