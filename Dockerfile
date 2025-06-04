FROM factoriotools/factorio:stable

ENV POETRY_VIRTUALENVS_CREATE=0
WORKDIR /factorio
COPY ./config config
COPY ./scripts scripts
COPY pyproject.toml poetry.lock ./
RUN apt update && \
    apt install --yes python3
RUN curl -sSL https://install.python-poetry.org | python3 - && \
    export PATH="/root/.local/bin:$PATH" && \
    poetry install --no-interaction --no-root

    

