const renderSOPCatalogBase=renderSOPCatalog;
renderSOPCatalog=function(){renderSOPCatalogBase();const success=document.querySelector('.overview-metric.success span');if(success)success.textContent='成功完成的运行';const successSmall=document.querySelector('.overview-metric.success small');if(successSmall)successSmall.textContent='累计成功次数'};
boot().catch(e=>{document.body.innerHTML=`<pre style="padding:30px">启动失败：${escapeHTML(e.message)}</pre>`});
