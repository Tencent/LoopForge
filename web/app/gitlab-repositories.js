function gitLabRepositoryInfo(repository){
  const webUrl=repository.web_url||repository.webUrl;
  return {
    provider:'gitlab',gitLabUrl:repository.gitLabUrl,gitLabProjectId:repository.gitLabProjectId||repository.id,
    name:repository.name,project:repository.path_with_namespace||webUrl?.replace(/^https?:\/\//,'')||'',
    webUrl,cloneUrl:repository.http_url_to_repo||repository.cloneUrl,
  };
}
