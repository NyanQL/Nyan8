const nyanAcceptedParams = {"addNumber": 2};
const nyanOutputColumns =  ["result"];


function main(){

    console.log(nyanAllParams.addNumber);
    console.log("console.log:" + typeof console.log);
    console.log("nyanHostExec:" + typeof nyanHostExec);
    console.log("nyanGetAPI:" + typeof nyanGetAPI);
    console.log("nyanJsonAPI:" + typeof nyanJsonAPI);
    console.log("nyanGetItem:" + typeof nyanGetItem);
    console.log("nyanSetItem:" + typeof nyanSetItem);
    console.log("nyanSetCookie:" + typeof nyanSetCookie);
    console.log("nyanGetCookie:" + typeof nyanGetCookie);
    //console.log("nyanHostExec result:" , nyanHostExec("echo 'test'; "));

    if (isDecimalNumber(nyanAllParams.addNumber)) {
        let result = parseFloat(2) + parseFloat(nyanAllParams.addNumber);
        return JSON.stringify({
            "success": true,
            "status": 201,
            "result": result
        });
    } else {
        return JSON.stringify({
            "success": false,
            "status": 500,
            "error": {
                "message": "addNumberは必須項目で、数値である必要があります。"
            },
        });
    }
}

function isDecimalNumber(value) {
    if (typeof value === "number") return true; // 数値型ならそのまま許容
    if (typeof value !== "string") return false; // 文字列でなければ false

    // 数字のみ（先頭の `0` を除外しない）で、小数点が1つまで
    if (!/^\d+(\.\d+)?$/.test(value)) return false;

    // 数値変換してチェック
    let num = Number(value);
    return Number.isFinite(num) && !isNaN(num);
}


main();