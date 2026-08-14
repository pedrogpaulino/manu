## Why

O Manu ainda está em descoberta e precisa de uma memória de projeto pequena, confiável e versionada antes que decisões de implementação se espalhem pelo código ou por conversas. A fundação documental deve preservar sua visão como plataforma que transforma fontes técnicas e documentais em uma base de conhecimento viva, sem reduzir o produto a um grafo, wiki, chat ou ferramenta de investigação isolados nem cristalizar cedo demais uma arquitetura definitiva.

## What Changes

- Criar um índice curto no `README.md`, apontando para as fontes de verdade do projeto e explicando como começar.
- Criar `PRODUCT.md` para registrar:
  - o problema de conhecimento fragmentado em ambientes empresariais grandes e legados;
  - sustentação, arquitetura e usuários de negócio como públicos iniciais;
  - o Knowledge Engine e a base de conhecimento viva como núcleo do produto;
  - catálogo, documentação/wiki, grafo, busca, chat, onboarding, análise de impacto e orientação de investigação como experiências construídas sobre esse núcleo;
  - o recorte do MVP, resultados esperados, não objetivos e hipóteses a validar.
- Criar `ARCHITECTURE.md` para registrar:
  - ingestão de código, arquivos, APIs, bancos, configurações e documentos existentes como fontes de primeira classe;
  - descoberta, parsing, correlação, evidências, proveniência, geração documental e curadoria como fluxo conceitual;
  - Manu Agent, monólito modular, PostgreSQL/pgvector e AI Gateway como partes da visão inicial;
  - políticas configuráveis de processamento, envio e visualização de conteúdo sensível;
  - a mesma plataforma operando futuramente como SaaS compartilhado, SaaS dedicado ou self-hosted, começando tenancy-ready e com uma organização por instalação;
  - restrições, atributos de qualidade e fronteiras arquiteturais, distinguindo decisões aceitas de opções futuras.
- Criar `DOMAIN.md` para estabelecer a linguagem inicial do Knowledge Engine e da colaboração, incluindo organização, fontes, artefatos, sistemas, entidades, relações, observações, afirmações de conhecimento, evidências, proveniência, páginas, revisões e curadoria, sem fechar antecipadamente o modelo físico de dados.
- Criar `AGENTS.md` como guia operacional conciso para colaboradores humanos e agentes, apontando para as fontes de verdade, comandos verificáveis e regras de manutenção.
- Criar `docs/decisions/` com instruções e um template leve de Architecture Decision Record (ADR), registrando novas decisões individualmente apenas quando forem realmente aceitas.
- Definir responsabilidades, regras de ligação e critérios de atualização para evitar duplicação e divergência entre documentos.
- Registrar especialistas como responsáveis por revisar, corrigir e enriquecer conhecimento para todos os usuários autorizados da organização, preservando conteúdo humano diante de novas análises automáticas.
- Manter integrações com ferramentas de chamados, ingestão de logs, métricas e traces, diagnóstico automático de causa raiz, SaaS compartilhado operacional e infraestrutura de control plane fora do MVP inicial.
- Manter roadmap detalhado, pesquisas, runbooks internos do próprio Manu e documentação de referência gerada fora da fundação do repositório até que exista uma necessidade concreta.

## Capabilities

### New Capabilities

Nenhuma. Esta é uma mudança exclusivamente documental e está marcada com `skip_specs: true`.

### Modified Capabilities

Nenhuma.

## Impact

- Afeta apenas a documentação e as convenções de colaboração do repositório.
- Não altera APIs, comportamento do produto, dependências, esquema de dados ou implantação.
- Cria a base que futuras mudanças OpenSpec e decisões arquiteturais deverão referenciar.
- Exige revisão editorial para garantir que conhecimento observado, gerado e curado, assim como fatos, decisões, hipóteses e opções futuras, estejam explicitamente separados.
