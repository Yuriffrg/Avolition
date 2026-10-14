import assert from 'node:assert/strict';
import {modelContext, matchClip, sortClipMatches, compareMatchScores, indexOccurrences, trackGroups} from '../web/clip-matching.js';

let checks = 0;
const same = (actual, expected, message) => { assert.deepEqual(actual, expected, message); checks++; };
const ids = (prefix, count) => Array.from({length:count}, (_, i) => `${prefix}_${i.toString().padStart(4, '0')}`);
const bodyIds = ids('body', 190), armsIds = ids('arms', 85), gunIds = ids('weapon', 19);
const base = 'C:/Library/Heroes/Example/Skin/Classic';
const model = (guid, boneNames, extra = {}) => ({id:guid, name:guid+'.00C', modelGuid:guid,
  path:`${base}/Models/${guid}.00C/${guid}.owmdl`, bones:boneNames.map(name=>({name})), parentId:null, ...extra});
const body = modelContext(model('ABC001',bodyIds,{entityName:'Gameplay3P'}),`${base}/Entities/Gameplay3P/Gameplay3P.owentity`);
const arms = modelContext(model('ABC002',armsIds,{entityName:'Gameplay1P'}),`${base}/Entities/Gameplay1P/Gameplay1P.owentity`);
const gun = modelContext(model('DEF001',gunIds,{parentId:'ABC002'}),`${base}/Entities/Gameplay1P/Gameplay1P.owentity`);
const clip = (name, boneNames, guid='ABC001', extra={}) => ({name, path:`${base}/Models/${guid}.00C/Animations/0/32/${name}.owanimclip`,
  boneCount:boneNames.length,binding:{boneNames},...extra});
same(body.totalBoneCount,190,'Context uses the actual skeleton length.');
same(body.boneNames instanceof Set,true,'Context retains exact source names.');
same(body.perspective,'Gameplay3P');same(arms.perspective,'Gameplay1P');same(gun.perspective,null,'A weapon child must not acquire the root entity perspective label.');
same(modelContext(model('X',gunIds,{entityName:'Generic'}),'C:/Other/Generic.owentity').perspective,null,'Counts do not infer semantic roles.');
same(modelContext(model('X',gunIds,{entityName:'Gameplay1P'}),'C:/Other/Gameplay3P.owentity').perspective,null,'Conflicting labels stay unknown.');
same(modelContext(null),null);

const partial = clip('partial_112',[...bodyIds.slice(0,108),...ids('other',4)]);
const partialScore = matchClip(partial,body);
same([partialScore.known,partialScore.matched,partialScore.totalBoneCount,partialScore.trackCount,partialScore.extraTracks],
  [true,108,190,112,4],'Partial tracks expose both skeleton and clip denominators.');
same(partialScore.coverage,108/190,'Coverage is of the selected skeleton, not108/112.');
same(partialScore.sameModelFolder,true);same(partialScore.sourceRank,1);
const near = clip('near_count',[...bodyIds.slice(0,186),...ids('other',4)]);
const larger = clip('larger_count',[...bodyIds,...ids('extra',51)]);
same([near.boneCount,larger.boneCount],[190,241]);
same([larger,partial,near].sort((a,b)=>sortClipMatches(a,b,body)).map(c=>c.name),
  ['larger_count','near_count','partial_112'],'More matched object bones outrank closeness to the skeleton track count.');
const gunGood = clip('gun_good',[...gunIds.slice(0,18),'unknown_weapon'],'DEF001');
const gunWrong = clip('other_nineteen',armsIds.slice(0,19),'ABC002');
same([gunGood.boneCount,gunWrong.boneCount],[19,19]);
same([matchClip(gunGood,gun).matched,matchClip(gunWrong,gun).matched],[18,0],'Equal counts do not imply the same joint identities.');
same(matchClip(gunWrong,gun).known,true,'Zero overlap is measured data, not a rejection.');
same(matchClip(gunGood,arms).sameModelFolder,false);

const otherHero = clip('other_hero',bodyIds,'ABC001',{
  path:'C:/Library/Heroes/Different/Skin/Classic/Models/ABC001.00C/Animations/0/32/other_hero.owanimclip'});
const otherSkin = clip('other_skin',bodyIds,'ABC001',{
  path:'C:/Library/Heroes/Example/Skin/Alternative/Models/ABC001.00C/Animations/0/32/other_skin.owanimclip'});
same(matchClip(otherHero,body).matched,190,'Another hero may share all joint names.');
same(matchClip(otherHero,body).sameModelFolder,false,'The same model token under another hero is not source-folder evidence.');
same(matchClip(otherSkin,body).sameModelFolder,false,'The same model token under another skin is a distinct export context.');
same([otherHero,partial].sort((a,b)=>sortClipMatches(a,b,body)).map(c=>c.name),['partial_112','other_hero'],
  'Verified same-object folder outranks incidental joint overlap on another source.');
const caseCopy = {...partial,path:partial.path.replaceAll('/','\\').toUpperCase()};
same(matchClip(caseCopy,body).sameModelFolder,true,'Windows slash and case differences preserve exact source provenance.');
same(matchClip({...partial,path:`${base}/Models/ABC001.00C-backup/Animations/0/32/A.owanimclip`},body).sameModelFolder,false,'Folder boundaries matter.');
same(matchClip({...partial,path:`${base}/Models/ABC001.00C/Textures/A.owanimclip`},body).sameModelFolder,false,'Only the actual model Animations subtree is evidence.');
same(modelContext({...model('ABC001',bodyIds),path:'Models/ABC001.00C/ABC001.owmdl'}).modelFolder,null,'Relative paths cannot establish a shared export root.');
same(modelContext({...model('ABC001',bodyIds),path:'C:/Unstructured/ABC001.owmdl'}).modelFolder,null,'Standalone model files still allow bone matching without guessed folder evidence.');
same(matchClip(partial,modelContext({...model('ABC001',bodyIds),path:'C:/Unstructured/ABC001.owmdl'})).matched,108);

const unknown = {...partial,name:'unknown',binding:undefined};
const failed = {...partial,name:'failed',binding:{error:'Permission denied'}};
same([matchClip(unknown,body).known,matchClip(unknown,body).matched,matchClip(unknown,body).coverage],[false,null,null],'Missing metadata never becomes a false zero.');
same(matchClip(failed,body).error,'Permission denied');
same(matchClip({...partial,error:'Bad file'},body).known,false,'Header/file errors remain unknown.');
same([unknown,otherHero].sort((a,b)=>sortClipMatches(a,b,body)).map(c=>c.name),['other_hero','unknown'],
  'Unknown metadata sorts below known even when its folder matches.');
same(matchClip(partial,null).known,false,'No chosen skeleton has no measured match.');
same(matchClip(partial,modelContext(model('none',[]))).known,false,'A skeletonless object has no match denominator.');
same(matchClip(clip('empty',[]),body).matched,0,'An explicitly empty clip is known zero tracks.');
same(matchClip({...partial,binding:{boneNames:[1,null]}},body).known,false,'Malformed metadata is unknown.');

const representative = {...otherHero,name:'renamed_alias',duplicateKey:'sha256:verified-content',binding:undefined};
const alias = {...partial,duplicateKey:representative.duplicateKey};
const aliasOther = {...partial,name:'same_filename_wrong_bytes',duplicateKey:'sha256:another-payload'};
const rows = [representative,alias,aliasOther], index = indexOccurrences(rows);
const original = JSON.stringify(rows);
for(const occurrences of [rows,index,[alias]]){
  const score = matchClip(representative,body,occurrences);
  same([score.known,score.matched,score.sameModelFolder],[true,108,true],'Byte-identical aliases retain original-folder and binding evidence.');
}
same(matchClip({...representative,duplicateKey:'sha256:no-alias'},body,rows).sameModelFolder,false,'Equal filenames do not borrow evidence.');
same(matchClip({...representative,duplicateError:'Hash unavailable'},body,rows).sameModelFolder,false,'Unverified duplicate groups do not borrow evidence.');
same(matchClip(representative,body,indexOccurrences([{...alias,error:'Unreadable'}])).known,false,'Broken aliases cannot supply unverified metadata.');
same(JSON.stringify(rows),original,'Matching leaves every source row, nickname and binding intact.');
alias.binding={boneNames:bodyIds};
same(matchClip(representative,body,index).matched,190,'Occurrence index references receive later binding updates without reindexing.');
alias.binding=partial.binding;
const minimal = clip('minimal',bodyIds.slice(0,108));
same(compareMatchScores(matchClip(minimal,body),partialScore)<0,true,'Equal matched bone counts prefer fewer extra clip tracks.');
const alpha = clip('alpha',bodyIds), zeta=clip('zeta',bodyIds);
same([zeta,alpha].sort((a,b)=>sortClipMatches(a,b,body)).map(c=>c.name),['alpha','zeta'],'Stable source names break equal scores.');
const cache = new Map([[alpha.path,matchClip(alpha,body)],[zeta.path,matchClip(zeta,body)]]);
same(sortClipMatches(alpha,zeta,null,[],cache),compareMatchScores(cache.get(alpha.path),cache.get(zeta.path)),'Sort accepts precomputed scores for large libraries.');
same(trackGroups([{boneCount:19},{boneCount:112},{boneCount:19},{boneCount:0},{boneCount:-1},{boneCount:1.2},{boneCount:'19'},{boneCount:NaN},{}]),
  [{tracks:112,count:1},{tracks:19,count:2},{tracks:0,count:1}],'Track groups count only actual valid integer headers; callers supply already visible rows.');
same(trackGroups([]),[]);
same(trackGroups([{boneCount:0,error:'Unreadable header'},{boneCount:19,binding:{error:'Unreadable keyframes'}},{boneCount:0}]),
  [{tracks:19,count:1},{tracks:0,count:1}], 'Unreadable headers are excluded, while valid counts remain useful when binding metadata is unavailable.');
same(trackGroups([gunGood,gunWrong]),[{tracks:19,count:2}],'Grouping is exact count only; it does not classify rigs.');
console.log(`Clip matching: ${checks} checks passed.`);
