## Context

O runtime atual produz um resultado local `v1alpha1` com manifesto, artefatos, contribuições, cobertura, lacunas, falhas e estado incremental. Uma `Contribution` já preserva artefato, analisador, método, valor estruturado e `Locator`, mas o resultado ainda não materializa uma sequência persistível de `Evidence Unit`s; o fallback genérico registra que não emitiu conteúdo. Consulte [proposal.md](proposal.md) para a motivação e [a delta spec](specs/evidence-backed-query/spec.md) para os comportamentos exigidos.

O corte precisa acrescentar uma plataforma consultável sem transformar o Agent em servidor, carregar arquivos inteiros para IA, quebrar a execução determinística sem banco ou criar serviços prematuros. `Organization` continua sendo a fronteira lógica mesmo com uma única organização e sem autenticação. O acesso local à fonte e a transferência externa permanecem autorizações independentes.

## Goals / Non-Goals

**Goals:**

- manter o Agent como produtor local de um bundle autocontido, limitado e verificável;
- criar uma fronteira HTTP estável entre Agent/cliente e plataforma;
- persistir conhecimento canônico antes de construir projeções de recuperação;
- responder perguntas de inventário, proveniência e abstinência com evidências inspecionáveis;
- permitir OpenAI e OpenRouter sem contaminar o domínio com DTOs de fornecedor;
- medir separadamente ingestão, recuperação, geração, custo e falhas;
- manter a célula local pequena, reproduzível e adequada ao futuro self-hosted.

**Non-Goals:**

- publicar a API em rede não confiável ou oferecer autenticação/autorização multiusuário;
- criar UI, streaming, conversação com memória, ferramentas de modelo ou acesso do modelo à fonte;
- persistir curadoria, wiki ou ciclo editorial;
- introduzir fila externa, Redis, MinIO, worker implantável separado ou plano de controle;
- aprofundar a semântica Java, WSO2 ou Python/Frappe além do necessário para materializar evidências já sustentadas;
- selecionar um único modelo como padrão comercial ou prometer equivalência entre provedores.

## Decisions

### 1. Agent e plataforma permanecem responsabilidades distintas no mesmo módulo Go

O comando `manu analyze` continua executando próximo à fonte e passa a produzir um `Analysis Bundle`. O novo comando `manu serve` compõe API, ingestão, persistência, recuperação e AI Gateway. Comandos `manu ask`, `manu evidence` e `manu eval` atuam como clientes da API para preservar automação e a superfície CLI prevista, sem duplicar a lógica da plataforma.

O primeiro repositório e módulo continuam monolíticos. A separação é por pacotes e portas internas, não por microserviços ou módulos Go independentes. O Agent não abre conexão direta com PostgreSQL e a API não recebe um caminho de filesystem da máquina do Agent.

Alternativas consideradas:

- **Servidor dentro do Agent:** simplificaria uma demonstração local, mas misturaria a fronteira confiável da fonte com persistência e credenciais externas.
- **Serviços separados desde já:** tornariam a implantação e a depuração mais caras antes de existir escala ou ciclo de vida independente.
- **Apenas CLI com acesso direto ao banco:** evitaria HTTP, mas não criaria o protocolo necessário para SaaS/self-hosted nem um contrato consumível por UI futura.

### 2. O Analysis Bundle é multipart em fluxo e não um arquivo-fonte compactado

O bundle estende o resultado operacional com um manifesto de bundle e `evidence.ndjson`. Na API, `POST /api/v1/ingestions` recebe partes limitadas para manifesto e sequências NDJSON, calcula um digest factual durante a leitura e valida referências antes da publicação. A primeira versão não aceita um repositório, diretório remoto ou arquivo compactado contendo a fonte.

Identidades do bundle incluem contrato, organização, fonte, snapshot, revisão/hash, configuração de análise e digest factual. O digest, combinado com organização, fonte e snapshot, fornece a chave natural de idempotência; um `Idempotency-Key` HTTP pode correlacionar tentativas, mas não substitui a identidade factual.

O Agent continua gravando arquivos atomicamente em destino separado. A mesma representação pode ser enviada pela CLI sem carregá-la integralmente em memória.

Alternativas consideradas:

- **ZIP/TAR da saída:** facilitaria transporte como um arquivo, mas exigiria outra superfície de traversal, expansão e armazenamento temporário neste corte.
- **JSON único:** seria simples para fixtures, mas perderia streaming e distorceria ERPNext.
- **Servidor ler `--source-root`:** só funcionaria quando Agent e plataforma compartilhassem filesystem e abriria uma autoridade implícita sobre a fonte.

### 3. Evidence Unit é o limite entre observação, recuperação e transferência

Uma `Evidence Unit` contém identidade estável, texto limitado ou estado de redação, hash do conteúdo, `Locator`, `Artifact`, `Analysis Snapshot`, contribuição/observação de origem, analisador, método e decisões de persistência e transferência. A unidade não é um `Knowledge Claim` e seu texto não ganha autoridade por ser indexado.

Analisadores especializados delimitam unidades por estrutura que conhecem: símbolo/método/configuração/exceção Java, elemento ou membro XML/WSO2 e relações declarativas. O fallback genérico usa blocos textuais limitados por parágrafo, seção ou janela de linhas, preservando a indicação de truncamento. Exclusões sensíveis atuais continuam valendo antes da leitura; sanitização e limites são aplicados antes de persistência ou transferência.

A política inicial possui decisões independentes para `persist` e `external_transfer`, cada uma com `allow`, `redact` ou `deny`. Como ainda não há usuário autenticado, a decisão combina configuração da instalação, configuração da fonte no bundle e classificação da unidade. Conteúdo proibido pode conservar hash e locador sem conservar texto.

Alternativas consideradas:

- **Chunk fixo por quantidade de caracteres/tokens:** é simples, mas separa código e documentos de sua unidade semântica e piora citação.
- **Arquivo inteiro:** aumenta recall aparente, custo e risco, além de contrariar a autorização mínima aceita.
- **Somente valores estruturados das contribuições:** preserva privacidade, mas não fornece suporte textual suficiente para embeddings, explicações ou inspeção humana.

### 4. PostgreSQL é a fonte de verdade operacional; pgvector é uma projeção

O schema inicial mantém, no mínimo, organizações, fontes, snapshots, artefatos, observações/contribuições, entidades, relações, evidências, cobertura, lacunas, falhas, jobs de ingestão, perfis de embedding, embeddings, consultas, candidatos, pacotes, afirmações geradas, citações e chamadas de provedor. Valores específicos de analisadores podem permanecer em JSON estruturado, enquanto identidades e relações consultáveis recebem colunas canônicas.

Snapshots são imutáveis. Uma fonte aponta para seu snapshot ativo, mas snapshots anteriores permanecem consultáveis. A atualização localizada cria novo snapshot e reutiliza identidades factuais compatíveis; projeções do snapshot ativo são atualizadas sem apagar o histórico.

Não será adotado ORM. Consultas são SQL parametrizadas e transações explícitas atrás de interfaces pequenas. Migrações SQL versionadas ficam embarcadas e são aplicadas por `manu migrate`; o Compose executa uma etapa única de migração antes de tornar a API pronta. Migrações destrutivas ou downgrade automático não entram no corte.

Alternativas consideradas:

- **SQLite:** é mais leve para uma CLI offline, mas diverge da célula SaaS/self-hosted e não valida pgvector ou concorrência da API.
- **Banco vetorial dedicado:** adiciona um serviço e outra fonte operacional sem necessidade demonstrada.
- **JSON como persistência consultável:** continua útil como bundle e reconstrução, mas não oferece transações, relações e consultas concorrentes adequadas à plataforma.

### 5. Ingestões usam um job durável no mesmo processo

`POST /api/v1/ingestions` valida envelope e limites iniciais, grava o job e responde `202 Accepted`. Um executor limitado no processo `manu serve` reivindica jobs no PostgreSQL, registra etapas e recupera trabalhos `pending` ou interrompidos após reinício. Não existe broker nem worker implantável separado.

As etapas são: validação integral, persistência canônica, projeção textual/relacional, embeddings autorizados e ativação do snapshot. Conhecimento canônico válido é confirmado antes da chamada externa. Falha de embedding resulta em `partial` e pode ser retomada; bundle inválido resulta em `failed` sem ativar snapshot parcial. Contadores e erros são persistidos sem conteúdo bruto.

Concorrência é limitada por configuração e por locação do job no banco. O desenho permite extrair um worker no futuro sem alterar o contrato HTTP ou as tabelas de estado, mas não cria essa unidade agora.

Alternativas consideradas:

- **Ingestão síncrona:** é menor, porém chamadas de embedding podem exceder timeouts e tornam retomada difícil.
- **Fila externa:** resolve distribuição antes de existir um segundo consumidor ou volume que a justifique.
- **Goroutine sem estado durável:** perde trabalho e torna o resultado ambíguo após reinício.

### 6. A API usa `net/http`, JSON versionado e erros estruturados

O servidor usa a biblioteca padrão Go para roteamento, limites, timeouts, cancelamento e encerramento gracioso. O contrato inicial é:

```text
POST /api/v1/ingestions
GET  /api/v1/ingestions/{ingestion_id}
POST /api/v1/queries
GET  /api/v1/queries/{query_id}
GET  /api/v1/evidence/{evidence_id}
GET  /healthz
GET  /readyz
```

Ingestão é assíncrona; consulta é síncrona no primeiro corte e persiste sua execução antes de responder. `GET /queries/{id}` permite reinspeção. Respostas carregam versão, identificadores e estado; falhas usam `application/problem+json` com código estável, detalhe seguro e correlação. Um documento OpenAPI descreve somente o contrato realmente implementado.

Sem autenticação, a configuração padrão é loopback e a validação recusa endereço não loopback. `healthz` indica processo vivo; `readyz` depende de configuração, conexão e versão de schema, mas não faz chamada de rede a um provedor. Request IDs, limites de corpo, deadlines e logs `slog` são obrigatórios.

Alternativas consideradas:

- **Framework HTTP:** economiza código de conveniência, mas a superfície pequena cabe na biblioteca padrão e evita dependência transversal.
- **GraphQL:** não oferece benefício para os poucos comandos iniciais e amplia o contrato antes do modelo estabilizar.
- **Streaming/SSE:** melhora experiência de respostas longas, mas exige estados e reconexão fora do primeiro teste.

### 7. As três projeções são reconstruíveis e a fusão é determinística

A projeção textual usa campos exatos e full-text com configuração apropriada a termos técnicos, sem exigir stemming de linguagem natural. A projeção relacional mantém entidades e relações dirigidas com expansão máxima de um salto e limites de fan-out. A projeção vetorial usa distância cosseno sobre um único perfil ativo de embedding por instalação.

O perfil é imutável e registra provedor, modelo, dimensão, normalização/configuração e versão. O cache de embedding usa perfil mais hash da `Evidence Unit`; trocar o gerador não invalida vetores, enquanto trocar o perfil de embedding cria nova projeção e exige reindexação. Vetores de perfis diferentes nunca participam da mesma ordenação.

O primeiro corpus usa busca vetorial exata para preservar recall e linha de base. Índice aproximado só será introduzido se volume e benchmark demonstrarem necessidade. Resultados lexical e vetorial são fundidos por ranking recíproco com pesos/configuração registrados; correspondência exata recebe sinal próprio. Relações diretas expandem o conjunto depois da seleção inicial, com orçamento separado. Não há reranker de modelo neste corte.

Alternativas consideradas:

- **Somar scores brutos:** combina escalas não comparáveis entre full-text e modelos de embedding.
- **Somente vetor:** perde símbolos exatos, degrada quando IA está proibida e transforma uma projeção em dependência epistemológica.
- **HNSW imediatamente:** pode melhorar latência, mas introduz parâmetros e aproximação antes de medir o conjunto real.

### 8. O pacote de evidências possui orçamento e isolamento de prompt

O recuperador produz candidatos com explicação de sinais. O compositor aplica limites de unidades, caracteres e estimativa de tokens; diversidade por artefato e tipo; escopo de organização/fonte/snapshot; política de transferência; deduplicação por hash; e inclusão das lacunas materiais. Ele registra candidatos aceitos, excluídos e motivo.

O pacote tem um schema próprio e é a única entrada organizacional do gerador. Evidências são delimitadas como dados não confiáveis, nunca como instruções. O gerador não recebe ferramentas, credenciais, conexão ao banco, diretório da fonte ou capacidade de buscar material adicional. Se o pacote não tiver suporte transferível, o núcleo produz abstinência determinística sem chamar o provedor.

Alternativas consideradas:

- **Enviar todos os candidatos:** maximiza contexto, mas aumenta custo, ruído, vazamento e domínio de um único artefato.
- **Permitir que o modelo consulte a fonte:** impede auditoria do contexto real e atravessa a política de acesso.
- **Usar apenas limite de tokens do provedor:** não garante diversidade nem reproduzibilidade entre modelos.

### 9. AI Gateway tem portas independentes e adaptadores explícitos

O domínio depende de duas portas pequenas: `Embedder`, para lotes de unidades, e `Generator`, para pergunta mais pacote de evidências. Requests e resultados internos carregam conteúdo autorizado, perfil, deadline, identificador de execução, modelo efetivo, uso, latência, término e erro normalizado; nenhum DTO de fornecedor sai do adaptador.

O adaptador OpenAI usa a API de embeddings e a Responses API para geração. O adaptador OpenAI-compatible usa endpoints configurados explicitamente para embeddings e geração, validado primeiro com OpenRouter. O protocolo (`responses` ou `chat_completions`) e as capacidades exigidas são declarados na configuração; não existe fallback silencioso entre protocolos ou modelos.

Embeddings e geração possuem configurações, credenciais, timeouts, lotes, modelos e orçamentos independentes. Modelos usados em avaliação são fixados por identificador configurado e o modelo efetivamente retornado é registrado. Aliases móveis não formam uma baseline comparável.

Erros são normalizados em autenticação, configuração/capacidade, limite, orçamento, rate limit, indisponibilidade, timeout/cancelamento, conteúdo bloqueado e resposta inválida. Retries limitados com backoff e jitter aplicam-se somente a falhas transitórias; cada tentativa e possível consumo são registrados. Credenciais vêm do ambiente/secret mount e nunca são serializadas.

Alternativas consideradas:

- **Usar diretamente os tipos OpenAI no núcleo:** acelera o primeiro adaptador, mas torna política, testes e troca de provedor dependentes de um contrato externo.
- **Tratar toda API compatível como idêntica:** ignora diferenças de parâmetros, recursos, erros e modelo efetivo.
- **Uma porta única para qualquer operação de IA:** mistura ciclo de vida de embeddings com geração e impede escolher provedores independentes.

### 10. A saída do gerador é estruturada e validada contra o pacote

O gerador deve retornar um envelope estruturado com resposta, afirmações, tipo (`observed`, `generated` ou `gap`), IDs de evidência citados e lacunas. O adaptador solicita saída estruturada somente de modelos que declarem essa capacidade; qualquer saída é decodificada e validada no núcleo.

O validador rejeita citações ausentes do pacote, identidades fora do escopo e afirmações relevantes sem suporte indicado. Uma saída inválida pode receber no máximo uma tentativa de reparo sob o mesmo orçamento; persistindo a invalidade, a consulta termina `partial` ou `failed` sem publicar texto livre como conhecimento confirmado. Relevância semântica da citação é medida na avaliação e não presumida por uma referência sintaticamente válida.

Consultas e respostas ficam registradas com pergunta, filtros, digest do pacote, claims, citações, modelo/configuração, uso, custo estimado, latência e estado. O corte não publica claims na base curada nem mantém memória conversacional.

Alternativas consideradas:

- **Texto livre com marcadores de citação:** é mais fácil para o modelo, mas difícil de validar e correlacionar por afirmação.
- **Segundo modelo como juiz obrigatório:** acrescenta custo e dependência sem substituir validação determinística ou referência humana.
- **Aceitar qualquer citação existente:** prova somente que o ID estava no pacote, não que sustenta a afirmação.

### 11. Simulação determinística e live eval usam o mesmo pipeline

Fixtures fornecem `Embedder` e `Generator` simulados, previsíveis e sem rede. A simulação percorre bundle, banco real de teste, projeções, recuperação, pacote, validação e resposta; não substitui apenas a camada que está sendo testada. Casos iniciais priorizam inventário, proveniência e abstinência no Ticketmaster, mantendo WSO2 e ERPNext para heterogeneidade, fallback e escala aplicáveis.

`live eval` exige flag explícita, configuração de provedor, política de transferência e orçamento de requisições/tokens/custo. O runner interrompe novas chamadas ao alcançar qualquer limite e registra hashes/IDs das evidências transferidas, nunca a chave. Relatórios atribuem falha a extração, ingestão/projeção, recuperação, geração ou política.

Métricas mínimas incluem latência por etapa, volume, evidências/embeddings reutilizados, recall e precisão de evidência em `k`, primeiro suporte relevante, claims sustentados, citações válidas, abstinência, tokens, custo e erros. Resultados locais não são SLA.

Alternativas consideradas:

- **Somente testes mockados de unidade:** não exercitam schema, SQL, ranking, políticas e contrato HTTP juntos.
- **OpenAI em toda suíte:** introduz custo, variação, rede e risco de conteúdo em testes padrão.
- **Avaliar apenas fluência da resposta:** não localiza se a falha veio de extração, recuperação ou geração.

### 12. Compose contém somente aplicação e PostgreSQL/pgvector

O Compose local usa imagem de aplicação Go e imagem PostgreSQL com pgvector, ambas fixadas em versões verificadas durante a aplicação. Serviços lógicos são banco, migração one-shot e API; migração e API reutilizam a mesma imagem Manu. A porta da API é publicada apenas em `127.0.0.1`, o banco não precisa ser publicado, e dados usam volume nomeado.

Configuração não secreta pode vir de arquivo de exemplo sem valores reais. Senhas e chaves são fornecidas externamente e arquivos locais correspondentes ficam ignorados pelo Git. A API pode iniciar sem provedor real para ingestão, recuperação não vetorial e testes simulados; prontidão não depende de disponibilidade remota da OpenAI/OpenRouter.

Alternativas consideradas:

- **MinIO desde já:** não há blob original ou produto documental que exija object storage neste corte.
- **Banco instalado no Agent:** aumenta consumo e mistura a ferramenta de leitura com a fonte de verdade da plataforma.
- **Imagem `latest`:** reduz trabalho imediato, mas torna schema, extensão e benchmark não reproduzíveis.

## Risks / Trade-offs

- **API sem autenticação ser exposta acidentalmente** → validar loopback de forma bloqueante, publicar Compose somente em `127.0.0.1` e documentar que rede remota exige mudança de autenticação.
- **Evidence Unit vazar segredo ou instrução maliciosa** → exclusões, sanitização, políticas independentes, limites, marcação como dado não confiável e nenhum acesso do modelo a ferramentas/fonte.
- **Contribuições atuais não sustentarem perguntas atraentes** → limitar aceitação inicial a inventário, proveniência e abstinência; registrar falha de extração antes de culpar RAG ou modelo.
- **Job no mesmo processo perder progresso** → estado e locação duráveis no PostgreSQL, operações idempotentes e recuperação de jobs após reinício.
- **Falha externa deixar ingestão inconsistente** → confirmar conhecimento canônico antes da projeção vetorial e usar estado `partial` retomável.
- **Mistura de embeddings incompatíveis** → um perfil ativo, chave completa de cache e rebuild explícito ao trocar perfil.
- **Compatibilidade OpenAI esconder diferenças de provedor** → protocolo e capacidades declarados, adaptadores isolados e testes contratuais por provedor.
- **Modelo citar evidência irrelevante apesar de ID válido** → validação sintática no runtime e avaliação de precisão/correção contra referências curadas.
- **PostgreSQL aumentar o consumo local** → manter apenas dois componentes, medir recursos no corpus e evitar serviços auxiliares até haver necessidade.
- **Schema canônico cristalizar cedo demais** → preservar payload especializado em JSON, normalizar somente conceitos já canônicos e versionar migrações/bundle/API.
- **Busca exata degradar com escala** → medir latência e recall; introduzir índice aproximado somente em mudança orientada por dados.

## Migration Plan

1. Verificar e fixar versões suportadas do PostgreSQL, pgvector e dependências Go; criar migrations aditivas e fixtures de integração.
2. Estender o contrato operacional com bundle/evidências sem quebrar leitura dos resultados `v1alpha1` já existentes; bundles sem evidência permanecem ingeríveis com recuperação limitada explícita.
3. Implementar persistência canônica e ingestão simulada antes de habilitar embeddings ou geração reais.
4. Adicionar projeções e recuperação textual/relacional, depois embeddings simulados e somente então provedores externos.
5. Publicar API/CLI e Compose local com loopback, executar casos determinísticos e reconstrução completa das projeções.
6. Executar `live eval` apenas sobre evidências autorizadas e sob orçamento, preservando a baseline anterior sem IA.

Rollback durante o experimento consiste em parar a nova célula e retornar ao Agent/CLI determinístico, que permanece independente. O volume PostgreSQL é descartável apenas em fixtures; qualquer migração aplicada a dados preservados exige backup e não recebe downgrade destrutivo automático.

## Open Questions

- Os identificadores exatos dos modelos OpenAI/OpenRouter, dimensões do embedding e limites de lote serão fixados após consulta aos catálogos oficiais no momento da implementação e registrados em cada execução.
- Os valores iniciais de `k`, pesos de fusão, fan-out relacional e orçamento do pacote serão calibrados com os casos curados; alterar esses valores gera nova configuração de avaliação, não mudança do contrato.
- O orçamento monetário padrão da `live eval` será escolhido antes da primeira chamada real; a suíte determinística e a implementação não dependem desse valor.
