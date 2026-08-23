## Why

O Manu já produz resultados determinísticos e rastreáveis do `Manu Agent`, mas ainda não consegue transformá-los em uma base consultável nem responder perguntas com evidências. Este corte estabelece o primeiro caminho de ponta a ponta entre análise, persistência, recuperação híbrida e resposta assistida por IA sem reduzir o conhecimento a vetores ou acoplar o núcleo a um único provedor.

## What Changes

- Introduz um `Analysis Bundle` versionado para o Agent enviar artefatos, contribuições, cobertura, lacunas, falhas e `Evidence Unit`s pequenas, semanticamente delimitadas e autorizadas, sem transferir arquivos inteiros por padrão.
- Adiciona uma API HTTP versionada ao monólito Go, iniciada por `manu serve`, para receber ingestões assíncronas, consultar seu estado, executar perguntas e inspecionar as evidências usadas.
- Mantém a API sem autenticação somente neste experimento local, vinculada a uma única `Organization` configurada e exposta em loopback por padrão; exposição remota e uso de produção permanecem bloqueados até existir autenticação em mudança própria.
- Persiste a fonte de verdade estruturada no PostgreSQL e cria projeções textual, relacional e vetorial reconstruíveis, usando pgvector para a projeção semântica e sem tratar embeddings como conhecimento ou evidência.
- Implementa recuperação híbrida limitada que combina termos, similaridade vetorial e relações diretas para formar um pacote autorizado de evidências antes de qualquer geração.
- Introduz o `AI Gateway` com portas independentes para embeddings e geração, configuração separada por capacidade, adaptador nativo OpenAI e adaptador compatível com OpenAI validado inicialmente com OpenRouter.
- Produz respostas identificadas como conhecimento gerado, com citações, proveniência, cobertura e lacunas; quando o pacote não sustentar a conclusão, a resposta deve se abster explicitamente.
- Mantém avaliação simulada como padrão e permite `live eval` explícita com orçamento, latência, modelo, tokens, custo e conteúdo transferido auditáveis, sem registrar credenciais.
- Acrescenta Docker Compose mínimo para a aplicação Manu e PostgreSQL/pgvector, sem MinIO, Redis, fila externa, worker separado ou interface web.
- Mantém fora do corte autenticação, exposição pública, streaming, curadoria/wiki, SaaS compartilhado, múltiplas organizações reais, adaptadores nativos adicionais e aprofundamento semântico dos analisadores.

## Capabilities

### New Capabilities

- `evidence-backed-query`: cobre ingestão HTTP do resultado do Agent, materialização e persistência de evidências, projeções reconstruíveis, recuperação híbrida, abstração de provedores de IA, resposta citada e avaliação do fluxo de consulta.

### Modified Capabilities

Nenhuma. O corte implementa comportamentos mais concretos para consulta já orientados por `knowledge-engine-comprehension` sem alterar seu contrato epistemológico, e preserva o runtime determinístico definido em `knowledge-engine-runtime`.

## Impact

- Afeta a composição e a CLI Go, acrescentando o modo servidor, clientes operacionais e módulos internos para API, ingestão, conhecimento, persistência, recuperação, AI Gateway e avaliação.
- Introduz PostgreSQL/pgvector, migrações de schema e Docker Compose como dependências operacionais locais; o Agent continua capaz de produzir seu resultado determinístico sem banco ou IA.
- Introduz contratos HTTP e de bundle versionados, além de configuração externa para banco, organização local, políticas de transferência, provedores, modelos e orçamentos.
- Estende fixtures e o corpus de referência com casos determinísticos de ingestão, recuperação, citação e abstinência; chamadas reais a provedores continuam opt-in.
- Pode exigir dependências Go externas para PostgreSQL e APIs de modelos, sujeitas à seleção, fixação de versão, revisão de segurança e validação na aplicação da mudança.
