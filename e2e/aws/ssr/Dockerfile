# 実 AWS E2E(SSR 側)の最小イメージ。アプリとしての中身は無い。
# 確かめたいのは「コンテナ配布の Lambda を kagerou が上げ下げできるか」で、
# ECR への push とイメージ参照の書き換えが絡む経路がここにしかない。
FROM public.ecr.aws/lambda/nodejs:22

COPY index.mjs ${LAMBDA_TASK_ROOT}/
CMD ["index.handler"]
