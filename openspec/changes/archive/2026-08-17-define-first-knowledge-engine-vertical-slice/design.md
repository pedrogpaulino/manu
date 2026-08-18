## Context

O repositório contém apenas a fundação documental do Manu. O contrato universal de compreensão está aceito, mas ainda não há stack, aplicação, modelo físico, comandos, dependências ou suíte de testes. A arquitetura vigente limita o início a um monólito modular, uma `Organization` por instalação, Docker Compose/VPS, PostgreSQL/pgvector como hipótese inicial e modelos acessados pelo `AI Gateway`.

O corte precisa transformar esses limites em um experimento comparável sem prometer compreensão profunda de Java, WSO2 e ERPNext ao mesmo tempo. Consulte [proposal.md](proposal.md) para motivação e [a especificação delta](specs/knowledge-engine-comprehension/spec.md) para o comportamento exigido.

## Goals / Non-Goals

**Goals:**

- definir a menor arquitetura capaz de percorrer fontes heterogêneas, preservar evidências, recuperar contexto e produzir uma resposta sustentada;
- separar claramente extração, recuperação e geração para localizar falhas e regressões;
- permitir evolução por analisadores especializados sem criar um engine ou modelo de dados independente para cada linguagem;
- usar as três bases disponíveis com papéis explícitos de correção, heterogeneidade e escala;
- produzir dados comparáveis para uma decisão posterior de stack, profundidade dos analisadores e custo operacional.

**Non-Goals:**

- escolher nesta mudança a linguagem, bibliotecas de parsing ou layout físico definitivo;
- oferecer análise semântica profunda de todas as linguagens, frameworks e artefatos do corpus;
- implementar UI, curadoria completa, documentação publicável, autenticação empresarial ou operação SaaS;
- permitir que o modelo consulte diretamente as fontes ou trate sua própria resposta como evidência;
- estabelecer SLA, capacidade comercial ou benchmark universal a partir de uma máquina de desenvolvimento.

## Decisions

### 1. Um pipeline comum recebe contribuições de analisadores compostos

O corte usará um único fluxo lógico:

```text
Source configurada
  → descoberta e snapshot
  → analisadores aplicáveis
  → observações, relações, evidências, cobertura e lacunas
  → projeções relacional, textual e vetorial
  → recuperação híbrida
  → pacote de evidências
  → AI Gateway
  → resposta gerada e rastreável
```

Mais de um analisador pode observar o mesmo artefato. Um analisador genérico fornece inventário, tipo, hash, localização e conteúdo textual aplicável; analisadores de linguagem, framework, pacote ou configuração acrescentam semântica. A correlação preserva quem produziu cada contribuição e não substitui silenciosamente resultados anteriores.

Alternativas consideradas:

- **engine separado por linguagem:** simplificaria cada implementação isolada, mas duplicaria ingestão, persistência, recuperação e avaliação;
- **um analisador universal centrado em IA:** reduziria o trabalho inicial de parsing, mas eliminaria previsibilidade, evidência determinística e operação parcial sem modelo;
- **apenas chunks vetoriais:** facilitaria um RAG demonstrativo, mas não sustentaria relações, cobertura, temporalidade nem investigação de falhas.

### 2. O corpus terá três faixas com profundidades diferentes

O corpus será referenciado por um manifesto versionado, sem copiar as bases para este repositório. O caminho físico será configuração local; o manifesto manterá identidade lógica, revisão ou hash, critérios de inclusão, exclusões, autorização e finalidade.

| Faixa | Recorte inicial | Profundidade esperada |
| --- | --- | --- |
| Correção semântica | fontes relevantes do Ticketmaster, excluindo relatórios gerados e material sensível | inventário, símbolos Java, endpoints, chamadas diretas, configurações referenciadas, exceções e `Possible Flow`s mínimos em Quarkus |
| Heterogeneidade | quatro a seis CARs representativos, selecionados por tipos distintos de artefato | abertura segura do pacote, inventário, artefatos WSO2 e referências diretas entre APIs, proxies, sequences, recursos, WSDL e XSD quando observáveis |
| Escala | inventário completo do ERPNext e recorte funcional de pedido a faturamento | fallback genérico no corte; profundidade Python/Frappe fica explicitamente não suportada até um analisador posterior |

O número exato de CARs pode variar sem mudar o design, desde que a seleção cubra tipos distintos, possua hashes e permaneça fixa dentro de uma execução comparativa.

Alternativa considerada: exigir a mesma profundidade nas três faixas. Foi rejeitada porque transformaria o primeiro experimento em três projetos de parser e esconderia o valor da cobertura progressiva.

### 3. O resultado mínimo preserva fatos antes das projeções de busca

Sem escolher tabelas ou serialização, cada execução precisa preservar ao menos:

- identidade da `Organization`, `Source`, `Source Revision` e `Analysis Snapshot`;
- identidade, tipo, localização e hash do `Artifact`;
- analisador, versão, método e instante de cada `Observation`;
- entidades e relações encontradas, com evidências de origem;
- unidades textuais semanticamente delimitadas e metadados necessários à recuperação;
- estado de `Analysis Coverage` e `Explicit Gap` por dimensão e escopo;
- falhas parciais sem invalidar contribuições concluídas;
- vínculo de cada embedding, índice textual ou expansão relacional com o conteúdo que projetou.

Embeddings e índices são projeções reconstruíveis. A base factual e sua proveniência não dependem deles.

Alternativa considerada: persistir somente documentos/chunks prontos para RAG. Foi rejeitada porque impediria explicar relações, reindexar com outro modelo e distinguir erro de extração de erro de recuperação.

### 4. A recuperação será híbrida e produzirá um pacote de evidências limitado

A consulta seguirá estas etapas conceituais:

1. validar organização, usuário, fonte e autorização de transferência;
2. formar candidatos por busca textual e similaridade vetorial;
3. expandir relações diretas relevantes sem atravessar indiscriminadamente o `System Graph`;
4. combinar e ordenar os sinais de forma reproduzível;
5. limitar por orçamento de evidências e contexto;
6. montar um pacote com trechos, entidades, relações, localizações, proveniência, cobertura e lacunas;
7. enviar somente o pacote autorizado ao `AI Gateway`;
8. validar a forma da resposta e seus identificadores de evidência antes de apresentá-la.

O modelo não terá acesso direto ao diretório analisado durante o `ask`. Isso evita que ele contorne o índice e permite avaliar a recuperação de maneira isolada.

Alternativas consideradas:

- **somente busca vetorial:** perde termos exatos, identificadores e relações estruturadas;
- **agente com acesso integral à fonte:** pode responder melhor em alguns casos, mas inviabiliza medir o conhecimento produzido pelo Manu e aumenta a superfície de acesso;
- **banco de grafo separado no primeiro corte:** acrescenta operação e sincronização antes de demonstrar necessidade; relações iniciais podem permanecer na fronteira de persistência considerada.

### 5. A OpenAI API será o adaptador externo inicial, não uma dependência do domínio

O `AI Gateway` terá portas distintas para embeddings e geração. A configuração inicial usará a OpenAI API, com modelos escolhidos por finalidade:

- um modelo de embedding econômico para indexação e consulta semântica;
- um modelo de geração econômico para smoke tests e desenvolvimento;
- um modelo mais capaz somente em avaliações periódicas de qualidade, quando autorizado.

Identificadores exatos de modelo, parâmetros e preços serão registrados em cada execução, não incorporados ao contrato do domínio. Avaliações comparáveis devem fixar a versão do modelo quando o provedor disponibilizar snapshot adequado.

A credencial será fornecida externamente ao processo, nunca registrada em manifesto, documento, saída, log ou fixture. O gateway registrará tokens, custo estimado, latência, estado e identificador da solicitação sem registrar conteúdo proibido. Antes de embeddings ou geração, o pacote passa por política e sanitização de segredos.

Alternativas consideradas:

- **Codex CLI como runtime:** é útil em experimentos pessoais, mas introduz comportamento agentivo e autenticação voltados ao desenvolvimento;
- **modelo gerativo local agora:** impediria transferência externa, mas acrescentaria requisitos de hardware e operação que o usuário decidiu adiar;
- **acoplamento direto à OpenAI:** reduziria uma interface inicial, mas espalharia política, telemetria e formato do provedor pelo engine.

### 6. A CLI exporá capacidades, não detalhes internos

A experiência inicial será organizada por intenções equivalentes a:

```text
source register   configurar uma fonte e suas políticas
analyze           criar um Analysis Snapshot
status            inspecionar progresso, cobertura, falhas e lacunas
ask               recuperar evidências e solicitar uma resposta
evidence          abrir o suporte de uma resposta ou relação
eval              executar perguntas de competência
benchmark         medir ingestão, consulta, recursos e custo
```

Os nomes finais podem ser ajustados na implementação, desde que as capacidades permaneçam disponíveis. Toda operação terá apresentação humana concisa e saída estruturada versionada. Falha parcial, ausência de IA e abstinência terão estados distinguíveis de erro técnico total.

Alternativa considerada: iniciar por API HTTP ou UI. A CLI oferece o menor caminho verificável, automatizável e adequado a benchmark sem antecipar experiência visual ou autenticação remota.

### 7. A avaliação será piramidal e baseada em referência revisável

Serão mantidas quatro classes de execução:

1. **fixtures determinísticas:** pequenos artefatos com entidades, relações, evidências e lacunas esperadas, sem API externa;
2. **contratos e integração local:** analisadores, persistência, reindexação e recuperação com provedores simulados;
3. **corpus de referência:** perguntas de competência com resposta e evidências esperadas, separando métricas de extração, recuperação e geração;
4. **live eval:** subconjunto autorizado com OpenAI API, limite de custo e registro completo da configuração não secreta.

As perguntas são instrumentos de avaliação, não regras codificadas para produzir respostas. Uma referência registra público, recorte, revisão, pergunta, afirmações aceitáveis, evidências esperadas, lacunas e autoria. A redação da IA não será comparada por igualdade textual.

Critérios mínimos:

- extração: presença e correção de entidades, relações, evidências, cobertura e lacunas;
- recuperação: evidências esperadas entre os primeiros candidatos, ruído e proveniência retornada;
- geração: correção, cobertura, rastreabilidade, incerteza e abstinência;
- operação: duração, pico de memória, armazenamento, tokens, custo e latência;
- evolução: diferença entre análise inicial, repetição sem mudança e atualização localizada.

Um avaliador baseado em modelo pode auxiliar, mas não substitui verificações determinísticas nem a referência humana.

Alternativa considerada: validar apenas a resposta final. Foi rejeitada porque uma resposta plausível pode ocultar parser incorreto, recuperação ruim ou uso indevido de conhecimento geral do modelo.

### 8. A escolha de stack será uma decisão posterior baseada no mesmo protocolo

Esta mudança define comportamento e benchmark antes de fixar runtime. Uma comparação de stack deverá executar um microcorte equivalente do manifesto e medir, no mínimo:

- descoberta, hashing e atualização de arquivos;
- parsing representativo de Java, XML/WSO2 e Python/texto;
- transformação para o resultado mínimo comum;
- escrita e leitura em lote pela fronteira de persistência;
- concorrência limitada, duração e pico de memória;
- empacotamento e operação em ambiente local compatível com o futuro Compose.

A decisão posterior deverá considerar conjuntamente desempenho, consumo, maturidade de bibliotecas, segurança de parsing, interoperabilidade dos analisadores e velocidade de evolução. Um benchmark sintético isolado não será suficiente.

Alternativa considerada: escolher a linguagem pela preferência ou por ranking genérico. Foi rejeitada porque o custo dominante e a adequação de bibliotecas só podem ser avaliados sobre os formatos e relações reais do Manu.

## Risks / Trade-offs

- **Fallback textual pode produzir respostas superficiais que parecem compreensão** → exibir cobertura por dimensão e testar relações/evidências, não somente fluência da resposta.
- **O recorte Java dominar o contrato comum** → usar CARs e ERPNext desde o início como testes de heterogeneidade e escala, mesmo com profundidade menor.
- **Fixtures favorecerem a implementação conhecida** → manter perguntas e referências versionadas, incluir casos negativos e reservar casos de regressão não usados durante o desenvolvimento imediato.
- **Resultados da IA variarem entre execuções** → fixar modelo e configuração nas avaliações comparáveis, preservar pacote de evidências e avaliar critérios independentes da redação.
- **Custos externos crescerem com reindexação ou suítes completas** → hashing e incrementalidade, orçamento por execução, provedores simulados por padrão e live eval explícita.
- **Código ou pacotes conterem segredos e instruções hostis** → seleção e sanitização antes da transferência, tratamento do conteúdo como dado não confiável e ausência de acesso direto do modelo à fonte.
- **Benchmark na máquina local ser confundido com capacidade comercial** → registrar ambiente e recorte e classificar os números como linha de base experimental, não SLA.
- **Adiar a stack postergar decisões de bibliotecas e packaging** → limitar o adiamento ao protocolo de benchmark definido e exigir decisão antes da mudança de implementação do corte completo.

## Migration Plan

Não há aplicação ou dados existentes a migrar. A aplicação desta mudança é documental: sincroniza a especificação principal e atualiza as fontes canônicas afetadas. A implementação futura deverá ocorrer em mudança separada, começando pelo manifesto e pelo benchmark de stack antes de materializar os componentes do corte.

Se o experimento invalidar uma hipótese, seus resultados, corpus e métricas serão preservados; a especificação ou o design deverão ser alterados por nova mudança OpenSpec, sem reclassificar retroativamente falhas como sucesso.
